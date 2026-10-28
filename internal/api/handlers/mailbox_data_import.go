package handlers

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxDataImport previews bounded metadata files and commits all valid
// records with their durable receipt in the same PostgreSQL transaction.
// Cancellation or a restart before commit rolls the entire mutation back.
type MailboxDataImport struct{ Pool *db.Pool }
type metadataImportRequest struct {
	Kind    string `json:"kind"`
	Format  string `json:"format"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
	Digest  string `json:"digest"`
}
type metadataImportError struct {
	Index int    `json:"index"`
	Key   string `json:"key"`
}
type metadataImportResult struct {
	Digest    string                `json:"digest"`
	Valid     int                   `json:"valid"`
	Added     int                   `json:"added"`
	Skipped   int                   `json:"skipped"`
	Errors    []metadataImportError `json:"errors"`
	Names     []string              `json:"names,omitempty"`
	Committed bool                  `json:"committed"`
}
type metadataRecord struct {
	Index   int
	Contact *collab.Contact
	Event   *collab.Event
}

func metadataDigest(in metadataImportRequest) string {
	sum := sha256.Sum256([]byte(in.Kind + "\x00" + in.Format + "\x00" + in.Mode + "\x00" + in.Content))
	return hex.EncodeToString(sum[:])
}

func parseMetadata(in metadataImportRequest) ([]metadataRecord, metadataImportResult, error) {
	result := metadataImportResult{Digest: metadataDigest(in), Errors: []metadataImportError{}, Names: []string{}}
	if len(in.Content) == 0 || len(in.Content) > 1<<20 || !utf8.ValidString(in.Content) || (in.Mode != "skip" && in.Mode != "keep_both") || (in.Kind != "contacts" && in.Kind != "calendar") {
		return nil, result, errors.New("invalid metadata import")
	}
	records := []metadataRecord{}
	add := func(index int, raw string) {
		var record metadataRecord
		record.Index = index
		if in.Kind == "contacts" {
			var contact collab.Contact
			var err error
			if in.Format == "csv" {
				var contacts []collab.Contact
				contacts, err = collab.ParseContactsCSV(raw)
				if err == nil {
					contact = contacts[0]
				}
			} else {
				contact, err = collab.ParseVCard(raw)
			}
			if err != nil {
				result.Errors = append(result.Errors, metadataImportError{index, "INVALID_PARAMS"})
				return
			}
			record.Contact = &contact
			result.Names = append(result.Names, contact.DisplayName)
		} else {
			var event collab.Event
			var err error
			if in.Format == "csv" {
				var events []collab.Event
				events, err = collab.ParseEventsCSV(raw)
				if err == nil {
					event = events[0]
				}
			} else {
				event, err = collab.ParseICalendar(raw)
			}
			if err != nil {
				result.Errors = append(result.Errors, metadataImportError{index, "INVALID_PARAMS"})
				return
			}
			record.Event = &event
			result.Names = append(result.Names, event.Summary)
		}
		records = append(records, record)
	}
	switch in.Format {
	case "csv":
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.Content, "\ufeff")))
		reader.FieldsPerRecord = -1
		for index := 1; ; index++ {
			row, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if index > 1001 {
				return nil, result, errors.New("metadata import too large")
			}
			if err != nil {
				result.Errors = append(result.Errors, metadataImportError{index, "INVALID_PARAMS"})
				continue
			}
			if index == 1 && len(row) > 0 && ((in.Kind == "contacts" && strings.EqualFold(row[0], "name")) || (in.Kind == "calendar" && strings.EqualFold(row[0], "summary"))) {
				continue
			}
			var encoded strings.Builder
			writer := csv.NewWriter(&encoded)
			if err = writer.Write(row); err != nil {
				return nil, result, err
			}
			writer.Flush()
			add(index, encoded.String())
		}
	case "vcf":
		if in.Kind != "contacts" {
			return nil, result, errors.New("wrong metadata format")
		}
		parts, err := metadataComponents(in.Content, "VCARD")
		if err != nil {
			return nil, result, err
		}
		for i, part := range parts {
			add(i+1, part)
		}
	case "ics":
		if in.Kind != "calendar" {
			return nil, result, errors.New("wrong metadata format")
		}
		parts, err := metadataComponents(in.Content, "VEVENT")
		if err != nil {
			return nil, result, err
		}
		// Group recurrence exceptions with their series UID before validation.
		grouped := map[string][]string{}
		order := []string{}
		for i, part := range parts {
			uid := ""
			flat := strings.ReplaceAll(part, "\r\n ", "")
			for _, line := range strings.Split(strings.ReplaceAll(flat, "\r\n", "\n"), "\n") {
				if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "UID") {
					uid = value
					break
				}
			}
			if uid == "" {
				uid = "missing-uid-" + strconv.Itoa(i)
			}
			if _, ok := grouped[uid]; !ok {
				order = append(order, uid)
			}
			grouped[uid] = append(grouped[uid], part)
		}
		for i, uid := range order {
			add(i+1, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"+strings.Join(grouped[uid], "")+"END:VCALENDAR\r\n")
		}
	default:
		return nil, result, errors.New("unsupported metadata format")
	}
	if len(records)+len(result.Errors) == 0 || len(records)+len(result.Errors) > 1000 {
		return nil, result, errors.New("empty or oversized metadata import")
	}
	result.Valid = len(records)
	return records, result, nil
}

func metadataComponents(raw, kind string) ([]string, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var current strings.Builder
	parts := []string{}
	inside := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		upper := strings.ToUpper(strings.TrimSpace(line))
		if upper == "BEGIN:"+kind {
			if inside {
				return nil, errors.New("nested metadata component")
			}
			inside = true
			current.Reset()
		}
		if inside {
			current.WriteString(line)
			current.WriteString("\r\n")
		}
		if upper == "END:"+kind {
			if !inside {
				return nil, errors.New("unmatched metadata component")
			}
			parts = append(parts, current.String())
			inside = false
			if len(parts) > 1000 {
				return nil, errors.New("too many metadata components")
			}
		}
	}
	if inside || len(parts) == 0 {
		return nil, errors.New("incomplete metadata component")
	}
	return parts, nil
}

// Handle validates a preview or atomically commits its reviewed metadata.
func (h MailboxDataImport) Handle(c *gin.Context) {
	sess, ok := middleware.GetMailboxSession(c)
	if !ok || h.Pool == nil {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 3<<20)
	var input metadataImportRequest
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	records, result, err := parseMetadata(input)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if c.Param("action") == "preview" {
		c.JSON(200, result)
		return
	}
	if c.Param("action") != "commit" || input.Digest != result.Digest {
		authError(c, 409, "CONFLICT")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if len(key) < 8 || len(key) > 200 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	ctx := c.Request.Context()
	tx, err := h.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockDataImport(ctx, store.PlatformLockDataImportParams{TenantID: strconv.FormatInt(sess.TenantID, 10), MailboxID: strconv.FormatInt(sess.MailboxID, 10), IdempotencyKey: "metadata-import"}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	kind := "import_" + input.Kind
	previous, err := q.PlatformGetDataImportReceipt(ctx, store.PlatformGetDataImportReceiptParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, Kind: kind, IdempotencyKey: key})
	if err == nil {
		var receipt metadataImportResult
		if json.Unmarshal(previous.Payload, &receipt) != nil || receipt.Digest != result.Digest {
			authError(c, 409, "CONFLICT")
			return
		}
		c.JSON(200, receipt)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "DB_ERROR")
		return
	}
	principal := appcollab.Principal{TenantID: sess.TenantID, MailboxID: sess.MailboxID}
	service := appcollab.Service{Q: q}
	var book, calendar int64
	if input.Kind == "contacts" {
		row, e := service.EnsureAddressbook(ctx, principal, "Contacts")
		err = e
		book = row.ID
	} else {
		row, e := service.EnsureCalendar(ctx, principal, defaultCalendarName, "UTC")
		err = e
		calendar = row.ID
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	for _, record := range records {
		if record.Contact != nil {
			contact := *record.Contact
			if input.Mode == "skip" {
				emails, _ := json.Marshal(contact.Emails)
				phones, _ := json.Marshal(contact.Phones)
				duplicate, e := q.PlatformContactImportDuplicate(ctx, store.PlatformContactImportDuplicateParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, DisplayName: contact.DisplayName, GivenName: contact.GivenName, FamilyName: contact.FamilyName, Column6: emails, Column7: phones, Note: contact.Note})
				if e != nil {
					authError(c, 503, "DB_ERROR")
					return
				}
				if duplicate {
					result.Skipped++
					continue
				}
			}
			if _, err = service.CreateContact(ctx, principal, book, contact); err != nil {
				authError(c, 503, "CONTACTS_UNAVAILABLE")
				return
			}
		} else {
			event := *record.Event
			if event.UID == "" && input.Mode == "skip" {
				canonical, _ := json.Marshal(event)
				fingerprint := sha256.Sum256(canonical)
				event.UID = "import-" + hex.EncodeToString(fingerprint[:16]) + "@urmail"
			}
			if event.UID != "" && input.Mode == "skip" {
				_, e := q.PlatformGetEventByUID(ctx, store.PlatformGetEventByUIDParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, Uid: event.UID})
				if e == nil {
					result.Skipped++
					continue
				}
				if !errors.Is(e, pgx.ErrNoRows) {
					authError(c, 503, "DB_ERROR")
					return
				}
			}
			if input.Mode == "keep_both" {
				event.UID = ""
			}
			if _, err = service.CreateEvent(ctx, principal, calendar, event.UID, event); err != nil {
				authError(c, 503, "CALENDAR_UNAVAILABLE")
				return
			}
		}
		result.Added++
	}
	result.Committed = true
	result.Names = nil
	receipt, _ := json.Marshal(result)
	if err = q.PlatformInsertDataImportReceipt(ctx, store.PlatformInsertDataImportReceiptParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, Kind: kind, IdempotencyKey: key, Payload: receipt}); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.JSON(200, result)
}

// History lists the authenticated mailbox's durable import receipts.
func (h MailboxDataImport) History(c *gin.Context) {
	sess, ok := middleware.GetMailboxSession(c)
	if !ok || h.Pool == nil {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	tx, err := h.Pool.MailboxTx(c, sess.TenantID, sess.MailboxID)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	rows, err := store.New(tx).PlatformListDataImportReceipts(c, store.PlatformListDataImportReceiptsParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	type history struct {
		ID        int64                `json:"id"`
		CreatedAt time.Time            `json:"created_at"`
		Kind      string               `json:"kind"`
		Key       string               `json:"idempotency_key"`
		Result    metadataImportResult `json:"result"`
	}
	out := []history{}
	for _, row := range rows {
		var result metadataImportResult
		if json.Unmarshal(row.Payload, &result) != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		out = append(out, history{ID: row.ID, CreatedAt: row.CreatedAt.Time, Kind: row.Kind, Key: row.IdempotencyKey, Result: result})
	}
	c.JSON(200, gin.H{"imports": out})
}

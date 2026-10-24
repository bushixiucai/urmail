package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// SupportHandler handles support content, without any mailbox-content authority.
type SupportHandler struct {
	Store, Platform *store.Queries
	PlatformPool    *db.Pool
}

func supportError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
	} else if !respondConstraintError(c, err) {
		authError(c, 500, "DB_ERROR")
	}
	return true
}
func supportCursor(c *gin.Context, key string, def int64) (int64, bool) {
	if c.Query(key) == "" {
		return def, true
	}
	v, e := strconv.ParseInt(c.Query(key), 10, 64)
	if e != nil || v < 0 {
		authError(c, 400, "INVALID_PARAMS")
		return 0, false
	}
	return v, true
}
func supportActor(c *gin.Context) (int64, int64, bool) {
	t, ok := marketingTenant(c)
	u, has := middleware.GetUserID(c)
	if !has {
		authError(c, 401, "UNAUTHORIZED")
	}
	return t, u, ok && has
}
func supportBody(s string) bool {
	return len(strings.TrimSpace(s)) > 0 && len(s) <= 65536 && !strings.ContainsRune(s, 0)
}

// List returns only the authenticated author's tickets, with keyset pagination.
func (h *SupportHandler) List(c *gin.Context) {
	t, u, ok := supportActor(c)
	if !ok {
		return
	}
	before, ok := supportCursor(c, "before_id", math.MaxInt64)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListSupportTickets(c, store.ListSupportTicketsParams{TenantID: t, OpenedBy: u, BeforeID: before})
	if supportError(c, err) {
		return
	}
	more := len(rows) > 50
	if more {
		rows = rows[:50]
	}
	c.JSON(200, gin.H{"tickets": rows, "has_more": more})
}

// Create never accepts an arbitrary author, staff flag or assignment.
func (h *SupportHandler) Create(c *gin.Context) {
	t, u, ok := supportActor(c)
	if !ok {
		return
	}
	var in struct{ Subject, Body, Priority string }
	if c.ShouldBindJSON(&in) != nil || !marketingNameValid(in.Subject) || !supportBody(in.Body) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if in.Priority != "low" && in.Priority != "normal" && in.Priority != "high" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	row, err := q.CreateSupportTicket(c, store.CreateSupportTicketParams{TenantID: t, OpenedBy: u, Subject: in.Subject, Priority: in.Priority})
	if supportError(c, err) {
		return
	}
	_, err = q.AddSupportMessage(c, store.AddSupportMessageParams{TenantID: t, TicketID: row.ID, AuthorID: u, Body: in.Body})
	if supportError(c, err) {
		return
	}
	c.JSON(201, gin.H{"ticket": row})
}

// Get returns escaped-by-renderer plain text, never executable rich content.
func (h *SupportHandler) Get(c *gin.Context) {
	t, u, ok := supportActor(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	after, ok := supportCursor(c, "after_id", 0)
	if !ok {
		return
	}
	q := requestStore(c, h.Store)
	ticket, err := q.GetSupportTicket(c, store.GetSupportTicketParams{TenantID: t, OpenedBy: u, ID: id})
	if supportError(c, err) {
		return
	}
	msgs, err := q.ListSupportMessages(c, store.ListSupportMessagesParams{TenantID: t, TicketID: id, AfterID: after})
	if supportError(c, err) {
		return
	}
	attachments, err := q.ListSupportAttachments(c, store.ListSupportAttachmentsParams{TenantID: t, TicketID: id})
	if supportError(c, err) {
		return
	}
	more := len(msgs) > 100
	if more {
		msgs = msgs[:100]
	}
	c.JSON(200, gin.H{"ticket": ticket, "messages": msgs, "attachments": attachments, "has_more": more})
}

type supportAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content"`
}
type supportReply struct {
	Body        string              `json:"body"`
	Attachments []supportAttachment `json:"attachments"`
}

func validSupportReply(in supportReply) bool {
	if !supportBody(in.Body) || len(in.Attachments) > 3 {
		return false
	}
	total := 0
	for _, a := range in.Attachments {
		total += len(a.Content)
		if len(a.Filename) < 1 || len(a.Filename) > 200 || strings.ContainsAny(a.Filename, "\r\n/\\\x00") || len(a.Content) == 0 || len(a.Content) > 2<<20 {
			return false
		}
		actual := strings.Split(http.DetectContentType(a.Content), ";")[0]
		if a.ContentType != "text/plain" && a.ContentType != "image/png" && a.ContentType != "image/jpeg" && a.ContentType != "application/pdf" {
			return false
		}
		if actual != a.ContentType {
			return false
		}
	}
	return total <= 2<<20
}
func addSupportAttachments(c *gin.Context, q *store.Queries, tid, mid int64, attachments []supportAttachment) error {
	for _, a := range attachments {
		_, err := q.AddSupportAttachment(c, store.AddSupportAttachmentParams{TenantID: tid, MessageID: mid, Filename: a.Filename, ContentType: a.ContentType, Content: a.Content})
		if err != nil {
			return err
		}
	}
	return nil
}

// Reply attaches bounded inert files to an authenticated participant's message.
func (h *SupportHandler) Reply(c *gin.Context) {
	t, u, ok := supportActor(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	var in supportReply
	if c.ShouldBindJSON(&in) != nil || !validSupportReply(in) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	row, err := q.AddSupportMessage(c, store.AddSupportMessageParams{TenantID: t, TicketID: id, AuthorID: u, Body: in.Body})
	if supportError(c, err) {
		return
	}
	if supportError(c, addSupportAttachments(c, q, t, row.ID, in.Attachments)) {
		return
	}
	c.JSON(201, gin.H{"message": row})
}

// Status lets the ticket author close or reopen their own conversation.
func (h *SupportHandler) Status(c *gin.Context) {
	t, u, ok := supportActor(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	var in struct{ Status string }
	if c.ShouldBindJSON(&in) != nil || (in.Status != "closed" && in.Status != "open") {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, err := requestStore(c, h.Store).ChangeSupportTicketStatus(c, store.ChangeSupportTicketStatusParams{TenantID: t, OpenedBy: u, ID: id, Status: in.Status})
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"ticket": row})
}

// Attachment always downloads with nosniff, including permitted image/PDF data.
func (h *SupportHandler) Attachment(c *gin.Context) {
	t, _, ok := supportActor(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "attachment_id")
	if !ok {
		return
	}
	row, err := requestStore(c, h.Store).GetSupportAttachment(c, store.GetSupportAttachmentParams{TenantID: t, ID: id})
	if supportError(c, err) {
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": row.Filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/octet-stream", row.Content)
}

// Announcements applies brand, publication and time restrictions in SQL and RLS.
func (h *SupportHandler) Announcements(c *gin.Context) {
	t, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListVisibleAnnouncements(c, t)
	if supportError(c, err) {
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, announcementDTO(row))
	}
	c.JSON(200, gin.H{"announcements": out})
}

// Help refers to the unique bilingual catalog, rather than maintaining a second copy.
func (h *SupportHandler) Help(c *gin.Context) {
	topics := []string{"dns", "renewal", "trial_refund", "ai", "archive_recall", "unsubscribe"}
	rows := make([]gin.H, 0, len(topics))
	for _, topic := range topics {
		rows = append(rows, gin.H{"id": topic, "title_key": "support.help." + topic + ".title", "body_key": "support.help." + topic + ".body"})
	}
	c.JSON(200, gin.H{"topics": rows})
}

// PlatformList runs only behind the platform support authorization boundary.
func (h *SupportHandler) PlatformList(c *gin.Context) {
	before, ok := supportCursor(c, "before_id", math.MaxInt64)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Platform).PlatformListSupportTickets(c, before)
	if supportError(c, err) {
		return
	}
	more := len(rows) > 50
	if more {
		rows = rows[:50]
	}
	c.JSON(200, gin.H{"tickets": rows, "has_more": more})
}

// PlatformGet reads ticket content, never archived mail or mailbox credentials.
func (h *SupportHandler) PlatformGet(c *gin.Context) {
	tid, ok := marketingID(c, "id")
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	after, ok := supportCursor(c, "after_id", 0)
	if !ok {
		return
	}
	row, err := requestStore(c, h.Platform).PlatformGetSupportTicket(c, store.PlatformGetSupportTicketParams{TenantID: tid, ID: id})
	if supportError(c, err) {
		return
	}
	msgs, err := requestStore(c, h.Platform).PlatformListSupportMessages(c, store.PlatformListSupportMessagesParams{TenantID: tid, TicketID: id, AfterID: after})
	if supportError(c, err) {
		return
	}
	attachments, err := requestStore(c, h.Platform).ListSupportAttachments(c, store.ListSupportAttachmentsParams{TenantID: tid, TicketID: id})
	if supportError(c, err) {
		return
	}
	more := len(msgs) > 100
	if more {
		msgs = msgs[:100]
	}
	c.JSON(200, gin.H{"ticket": row, "messages": msgs, "attachments": attachments, "has_more": more})
}

// PlatformReply binds staff identity to the session and commits its notification atomically.
func (h *SupportHandler) PlatformReply(c *gin.Context) {
	tid, ok := marketingID(c, "id")
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	uid, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	var in supportReply
	if c.ShouldBindJSON(&in) != nil || !validSupportReply(in) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if h.PlatformPool == nil {
		authError(c, 503, "SUPPORT_UNAVAILABLE")
		return
	}
	q, finish, rollback, err := platformMutationQueries(c, h.PlatformPool)
	if supportError(c, err) {
		return
	}
	defer rollback()
	ticket, err := q.PlatformGetSupportTicket(c, store.PlatformGetSupportTicketParams{TenantID: tid, ID: id})
	if supportError(c, err) {
		return
	}
	middleware.SetPlatformAuditTenant(c, ticket.TenantID)
	if ticket.Status == "closed" || ticket.Status == "resolved" {
		authError(c, 409, "TICKET_CLOSED")
		return
	}
	row, err := q.PlatformReplySupportTicket(c, store.PlatformReplySupportTicketParams{TenantID: tid, TicketID: id, AuthorID: uid, Body: in.Body})
	if supportError(c, err) {
		return
	}
	if supportError(c, addSupportAttachments(c, q, tid, row.ID, in.Attachments)) {
		return
	}
	if supportError(c, finish()) {
		return
	}
	c.JSON(201, gin.H{"message": row})
}

// PlatformUpdate validates both the workflow state and the assigned support operator.
func (h *SupportHandler) PlatformUpdate(c *gin.Context) {
	tid, ok := marketingID(c, "id")
	if !ok {
		return
	}
	id, ok := marketingID(c, "ticket_id")
	if !ok {
		return
	}
	var in struct {
		Status     string `json:"status"`
		AssignedTo *int64 `json:"assigned_to"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Status != "open" && in.Status != "pending" && in.Status != "resolved" && in.Status != "closed" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	assigned := pgtype.Int8{}
	if in.AssignedTo != nil {
		assigned = pgtype.Int8{Int64: *in.AssignedTo, Valid: true}
	}
	row, err := requestStore(c, h.Platform).PlatformChangeSupportTicket(c, store.PlatformChangeSupportTicketParams{TenantID: tid, ID: id, Status: in.Status, AssignedTo: assigned})
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"ticket": row})
}

// PlatformAnnouncements includes draft and withdrawn content for trusted editors.
func (h *SupportHandler) PlatformAnnouncements(c *gin.Context) {
	rows, err := requestStore(c, h.Platform).PlatformListAnnouncements(c)
	if supportError(c, err) {
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, announcementDTO(row))
	}
	c.JSON(200, gin.H{"announcements": out})
}

// SaveAnnouncement stores bilingual plain text, making markup inert in all renderers.
func (h *SupportHandler) SaveAnnouncement(c *gin.Context) {
	var in struct {
		BrandID     *int64            `json:"brand_id"`
		Title       map[string]string `json:"title"`
		Body        map[string]string `json:"body"`
		State       string            `json:"state"`
		PublishedAt *time.Time        `json:"published_at"`
		EndsAt      *time.Time        `json:"ends_at"`
	}
	if c.ShouldBindJSON(&in) != nil || in.State != "draft" && in.State != "published" && in.State != "withdrawn" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	for _, lang := range []string{"zh-CN", "en"} {
		if !marketingNameValid(in.Title[lang]) || !supportBody(in.Body[lang]) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	published, ends := pgtype.Timestamptz{}, pgtype.Timestamptz{}
	if in.PublishedAt != nil {
		published = pgtype.Timestamptz{Time: *in.PublishedAt, Valid: true}
	}
	if in.EndsAt != nil {
		ends = pgtype.Timestamptz{Time: *in.EndsAt, Valid: true}
	}
	if in.State == "published" && !published.Valid || ends.Valid && (!published.Valid || !ends.Time.After(published.Time)) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	brand := pgtype.Int8{}
	if in.BrandID != nil {
		brand = pgtype.Int8{Int64: *in.BrandID, Valid: true}
	}
	title, _ := json.Marshal(in.Title)
	body, _ := json.Marshal(in.Body)
	if c.Param("announcement_id") == "" {
		row, err := requestStore(c, h.Platform).PlatformCreateAnnouncement(c, store.PlatformCreateAnnouncementParams{BrandID: brand, Title: title, Body: body, State: in.State, PublishedAt: published, EndsAt: ends})
		if supportError(c, err) {
			return
		}
		c.JSON(201, gin.H{"announcement": announcementDTO(row)})
		return
	}
	id, ok := marketingID(c, "announcement_id")
	if !ok {
		return
	}
	row, err := requestStore(c, h.Platform).PlatformUpdateAnnouncement(c, store.PlatformUpdateAnnouncementParams{ID: id, BrandID: brand, Title: title, Body: body, State: in.State, PublishedAt: published, EndsAt: ends})
	if supportError(c, err) {
		return
	}
	c.JSON(200, gin.H{"announcement": announcementDTO(row)})
}

// PlatformAttachment uses the same inert download policy under explicit support authorization.
func (h *SupportHandler) PlatformAttachment(c *gin.Context) {
	tid, ok := marketingID(c, "id")
	if !ok {
		return
	}
	id, ok := marketingID(c, "attachment_id")
	if !ok {
		return
	}
	row, err := requestStore(c, h.Platform).GetSupportAttachment(c, store.GetSupportAttachmentParams{TenantID: tid, ID: id})
	if supportError(c, err) {
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": row.Filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/octet-stream", row.Content)
}

func announcementDTO(row store.Announcements) gin.H {
	return gin.H{"id": row.ID, "brand_id": row.BrandID, "title": json.RawMessage(row.Title), "body": json.RawMessage(row.Body), "state": row.State, "published_at": row.PublishedAt, "ends_at": row.EndsAt, "created_at": row.CreatedAt}
}

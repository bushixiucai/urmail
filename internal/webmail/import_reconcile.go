package webmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/mail"
	"strconv"
	"strings"
)

// ImportCopyFinder verifies exact complete MIME bytes in one pinned folder.
// Message-ID is only a discovery hint, never the equality or acceptance proof.
type ImportCopyFinder interface {
	FindImportCopies(context.Context, string, []byte, uint32) ([]DraftSaveResult, error)
}

type importBudgetKey struct{}
type importReadBudget struct{ remaining int64 }

// WithImportReadBudget shares a bounded byte budget across one serial preview.
func WithImportReadBudget(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, importBudgetKey{}, &importReadBudget{remaining: limit})
}

// FindImportCopies refuses incomplete candidate sets, changed generations and
// exhausted budgets rather than calling an unobserved message a nonduplicate.
func (c *imapConn) FindImportCopies(ctx context.Context, folder string, raw []byte, validity uint32) ([]DraftSaveResult, error) {
	if validity == 0 {
		return nil, errors.New("invalid import generation")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return nil, err
	}
	observedValidity, next, err := c.importMailboxIdentity(ctx, folder)
	if err != nil || observedValidity != validity {
		return nil, errors.New("import generation changed or identity unavailable")
	}
	criteria := "ALL"
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if id := message.Header.Get("Message-ID"); id != "" {
		if len(id) > 998 || strings.ContainsAny(id, "\r\n\x00") {
			return nil, errors.New("invalid import identity")
		}
		criteria = "HEADER Message-ID " + quoteIMAP(id)
	}
	if next > 1 {
		criteria += " UID 1:" + strconv.FormatUint(uint64(next-1), 10)
	}
	candidates, err := c.importUIDSearch(ctx, criteria)
	if err != nil {
		return nil, err
	}
	if len(candidates) > 100 {
		return nil, errors.New("import candidate bound exceeded")
	}
	budget, _ := ctx.Value(importBudgetKey{}).(*importReadBudget)
	if budget == nil {
		budget = &importReadBudget{remaining: 32 << 20}
		ctx = context.WithValue(ctx, importBudgetKey{}, budget)
	}
	digest := sha256.Sum256(raw)
	result := []DraftSaveResult{}
	for _, uid := range candidates {
		if uid >= next {
			return nil, errors.New("import SEARCH escaped UIDNEXT watermark")
		}
		if budget.remaining <= 0 {
			return nil, errors.New("import byte budget exceeded")
		}
		found, current, readErr := c.GetRawMessage(ctx, folder, uid)
		if readErr != nil {
			return nil, readErr
		}
		budget.remaining -= int64(len(found))
		if budget.remaining < 0 {
			return nil, errors.New("import byte budget exceeded")
		}
		if current != validity {
			return nil, errors.New("import generation changed")
		}
		if sha256.Sum256(found) == digest {
			result = append(result, DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: current})
		}
	}
	if current, after, e := c.importMailboxIdentity(ctx, folder); e != nil || current != validity || after != next {
		return nil, errors.New("import UIDNEXT or generation changed during inventory")
	}

	return result, nil
}

// importMailboxIdentity requires both identity and an arrival watermark. A
// changing UIDNEXT makes an absent result incomplete, never retry authority.
func (c *imapConn) importMailboxIdentity(ctx context.Context, folder string) (uint32, uint32, error) {
	finish := c.guardIO(ctx)
	defer finish()
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" STATUS "+quoteIMAP(encodeMailboxWire(folder))+" (UIDVALIDITY UIDNEXT)\r\n"); err != nil {
		return 0, 0, err
	}
	var validity, next uint32
	responses := 0
	for lines := 0; lines < 1000; lines++ {
		line, err := c.readLogicalLine()
		if err != nil {
			return 0, 0, err
		}
		if strings.HasPrefix(line, "* STATUS ") {
			name, rest, ok := readIMAPString(strings.TrimSpace(strings.TrimPrefix(line, "* STATUS ")))
			if !ok || name != encodeMailboxWire(folder) && (!strings.EqualFold(name, "INBOX") || !strings.EqualFold(folder, "INBOX")) {
				return 0, 0, errors.New("import STATUS folder mismatch")
			}
			rest = strings.TrimSpace(rest)
			if !strings.HasPrefix(rest, "(") || !strings.HasSuffix(rest, ")") {
				return 0, 0, errors.New("invalid import STATUS")
			}
			fields := strings.Fields(rest[1 : len(rest)-1])
			if len(fields) != 4 {
				return 0, 0, errors.New("incomplete import STATUS")
			}
			for i := 0; i < len(fields); i += 2 {
				value, e := strconv.ParseUint(fields[i+1], 10, 32)
				if e != nil || value == 0 {
					return 0, 0, errors.New("invalid import STATUS identity")
				}
				switch strings.ToUpper(fields[i]) {
				case "UIDVALIDITY":
					if validity != 0 {
						return 0, 0, errors.New("duplicate import STATUS identity")
					}
					validity = uint32(value)
				case "UIDNEXT":
					if next != 0 {
						return 0, 0, errors.New("duplicate import STATUS watermark")
					}
					next = uint32(value)
				default:
					return 0, 0, errors.New("unexpected import STATUS field")
				}
			}
			responses++
		}
		if strings.HasPrefix(line, tag+" OK") {
			if responses != 1 || validity == 0 || next == 0 {
				return 0, 0, errors.New("missing complete import STATUS")
			}
			return validity, next, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return 0, 0, errors.New("import STATUS rejected")
		}
	}
	return 0, 0, errors.New("import STATUS response bound exceeded")
}

func (c *imapConn) importUIDSearch(ctx context.Context, criteria string) ([]uint32, error) {
	finish := c.guardIO(ctx)
	defer finish()
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" UID SEARCH "+criteria+"\r\n"); err != nil {
		return nil, err
	}
	result := []uint32{}
	responses := 0
	seen := map[uint32]bool{}
	for lines := 0; lines < 1000; lines++ {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, err
		}
		if line == "* SEARCH" || strings.HasPrefix(line, "* SEARCH ") {
			responses++
			for _, field := range strings.Fields(strings.TrimPrefix(line, "* SEARCH")) {
				value, e := strconv.ParseUint(field, 10, 32)
				if e != nil || value == 0 || seen[uint32(value)] {
					return nil, errors.New("incomplete import SEARCH identity")
				}
				seen[uint32(value)] = true
				result = append(result, uint32(value))
				if len(result) > 100 {
					return nil, errors.New("import SEARCH candidate budget exceeded")
				}
			}
		}
		if strings.HasPrefix(line, "* ESEARCH") {
			return nil, errors.New("unsupported partial import SEARCH")
		}
		if strings.HasPrefix(line, tag+" OK") {
			if responses != 1 {
				return nil, errors.New("missing complete import SEARCH")
			}
			return result, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, errors.New("import SEARCH rejected")
		}
	}
	return nil, errors.New("import SEARCH response bound exceeded")
}

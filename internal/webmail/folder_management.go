package webmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FolderManager is an optional capability for mailbox namespace changes.
type FolderManager interface {
	CreateFolder(context.Context, string) error
	RenameFolder(context.Context, string, string) error
	DeleteFolder(context.Context, string) error
	TransferMessages(context.Context, string, []uint32, string, bool) (TransferResult, error)
}

// TransferItem records only outcomes acknowledged by UIDPLUS, never guessed.
type TransferItem struct {
	UID            uint32 `json:"uid"`
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	DestinationUID uint32 `json:"destination_uid,omitempty"`
}

// TransferResult binds destination UIDs to their authoritative generation.
type TransferResult struct {
	Results     []TransferItem `json:"results"`
	Destination string         `json:"destination"`
	NameEnc     string         `json:"name_enc"`
	UIDValidity uint32         `json:"uid_validity"`
	Atomic      bool           `json:"atomic"`
}

// ValidManagedFolder rejects protocol controls and misleading empty segments.
func ValidManagedFolder(name string) bool {
	if strings.TrimSpace(name) != name || name == "" || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimSpace(part) == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ProtectedFolder reports server special-use namespaces that cannot be renamed
// or removed by ordinary folder management.
func ProtectedFolder(folder Folder) bool {
	for _, name := range []string{"INBOX", "Sent", "Drafts", "Trash", "Junk", "Spam", "Archive", "Archives"} {
		if strings.EqualFold(folder.Name, name) {
			return true
		}
	}
	for _, flag := range folder.Flags {
		switch strings.ToLower(flag) {
		case `\inbox`, `\sent`, `\drafts`, `\trash`, `\junk`, `\archive`, `\all`, `\important`, `\flagged`:
			return true
		}
	}
	return false
}

func (c *imapConn) CreateFolder(ctx context.Context, name string) error {
	if !ValidManagedFolder(name) {
		return ErrBadFolderName
	}
	if err := c.command(ctx, "CREATE", quoteIMAP(encodeMailboxWire(name))); err != nil {
		return err
	}
	_ = c.command(ctx, "SUBSCRIBE", quoteIMAP(encodeMailboxWire(name)))
	return nil
}
func (c *imapConn) RenameFolder(ctx context.Context, oldName, newName string) error {
	if !ValidManagedFolder(oldName) || !ValidManagedFolder(newName) || oldName == newName {
		return ErrBadFolderName
	}
	if err := c.command(ctx, "RENAME", quoteIMAP(encodeMailboxWire(oldName)), quoteIMAP(encodeMailboxWire(newName))); err != nil {
		return err
	}
	// Subscription changes are best effort after authoritative namespace rename.
	_ = c.command(ctx, "UNSUBSCRIBE", quoteIMAP(encodeMailboxWire(oldName)))
	_ = c.command(ctx, "SUBSCRIBE", quoteIMAP(encodeMailboxWire(newName)))
	return nil
}
func (c *imapConn) DeleteFolder(ctx context.Context, name string) error {
	if !ValidManagedFolder(name) {
		return ErrBadFolderName
	}
	// UNSELECT never expunges another client's deleted messages. Deleting a
	// selected Dovecot mailbox would otherwise disconnect this IMAP session.
	if err := c.command(ctx, "UNSELECT"); err != nil {
		return err
	}
	return c.command(ctx, "DELETE", quoteIMAP(encodeMailboxWire(name)))
}

func expandUIDPlusSet(value string) ([]uint32, error) {
	out := []uint32{}
	for _, item := range strings.Split(value, ",") {
		ends := strings.Split(item, ":")
		if len(ends) > 2 {
			return nil, ErrBadFolderName
		}
		first, err := strconv.ParseUint(ends[0], 10, 32)
		if err != nil || first == 0 {
			return nil, ErrBadFolderName
		}
		last := first
		if len(ends) == 2 {
			last, err = strconv.ParseUint(ends[1], 10, 32)
			if err != nil || last < first {
				return nil, ErrBadFolderName
			}
		}
		if last-first+1 > MaxPageSize || len(out)+int(last-first+1) > MaxPageSize {
			return nil, ErrBadFolderName
		}
		for n := first; n <= last; n++ {
			if n > 0xffffffff {
				return nil, ErrBadFolderName
			}
			out = append(out, uint32(n))
		}
	}
	return out, nil
}

func (c *imapConn) TransferMessages(ctx context.Context, folder string, uids []uint32, dest string, move bool) (TransferResult, error) {
	result := TransferResult{Destination: dest, NameEnc: EncodeFolderName(dest), Results: make([]TransferItem, 0, len(uids))}
	for _, uid := range uids {
		result.Results = append(result.Results, TransferItem{UID: uid, Status: "unknown", Code: "TRANSFER_UNCERTAIN"})
	}
	if len(uids) == 0 || len(uids) > MaxPageSize || !ValidManagedFolder(dest) || folder == dest {
		return result, ErrBadFolderName
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return result, err
	}
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	verb := "UID COPY"
	if move {
		verb = "UID MOVE"
	}
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" "+verb+" "+formatUIDSet(uids)+" "+quoteIMAP(encodeMailboxWire(dest))+"\r\n"); err != nil {
		return result, err
	}
	acknowledged := map[uint32]uint32{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return result, err
		}
		if index := strings.Index(line, "[COPYUID "); index >= 0 {
			fields := strings.Fields(strings.SplitN(line[index+9:], "]", 2)[0])
			if len(fields) == 3 {
				validity, e := strconv.ParseUint(fields[0], 10, 32)
				src, se := expandUIDPlusSet(fields[1])
				dst, de := expandUIDPlusSet(fields[2])
				if e != nil || validity == 0 || se != nil || de != nil || len(src) != len(dst) {
					return result, errors.New("invalid UIDPLUS acknowledgement")
				}
				result.UIDValidity = uint32(validity)
				for i, uid := range src {
					acknowledged[uid] = dst[i]
				}
			}
		}
		if strings.HasPrefix(line, tag+" OK") {
			for i, item := range result.Results {
				if target, ok := acknowledged[item.UID]; ok {
					result.Results[i] = TransferItem{UID: item.UID, Status: "success", DestinationUID: target}
				} else if result.UIDValidity != 0 {
					result.Results[i] = TransferItem{UID: item.UID, Status: "failed", Code: "MESSAGE_NOT_FOUND"}
				}
			}
			return result, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return result, fmt.Errorf("%s", strings.TrimSpace(line))
		}
	}
}

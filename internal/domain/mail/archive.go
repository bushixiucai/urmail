package mail

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ArchiveRecipient binds delayed copies to immutable mailbox ownership.
type ArchiveRecipient struct {
	TenantID  int64
	MailboxID int64
	Address   string
}

// EncodeArchiveRecipient produces a trusted generated BCC envelope destination.
func EncodeArchiveRecipient(tenantID, mailboxID int64, address string) string {
	payload := strings.ToLower(address)
	if tenantID > 0 && mailboxID > 0 {
		payload = fmt.Sprintf("v2:%d:%d:%s", tenantID, mailboxID, payload)
	}
	return "archive+" + hex.EncodeToString([]byte(payload)) + "@archive.invalid"
}

// DecodeArchiveRecipient accepts legacy recipients only for immediate online binding.
func DecodeArchiveRecipient(recipient string) (ArchiveRecipient, error) {
	var result ArchiveRecipient
	if !strings.HasPrefix(recipient, "archive+") || !strings.HasSuffix(recipient, "@archive.invalid") {
		return result, errors.New("invalid archive recipient")
	}
	payload, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(recipient, "archive+"), "@archive.invalid"))
	if err != nil {
		return result, err
	}
	address := string(payload)
	if strings.HasPrefix(address, "v2:") {
		parts := strings.SplitN(address, ":", 4)
		if len(parts) != 4 {
			return result, errors.New("invalid archive identity")
		}
		result.TenantID, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || result.TenantID <= 0 {
			return result, errors.New("invalid archive tenant")
		}
		result.MailboxID, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil || result.MailboxID <= 0 {
			return result, errors.New("invalid archive mailbox")
		}
		address = parts[3]
	}
	if len(address) > 320 || strings.ContainsAny(address, "\r\n\x00 ") || strings.Count(address, "@") != 1 {
		return result, errors.New("invalid archive address")
	}
	result.Address = strings.ToLower(address)
	return result, nil
}

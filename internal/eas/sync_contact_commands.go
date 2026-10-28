package eas

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// parseContactWrite applies only represented properties. Unsupported nonempty
// fields are rejected, so a client never receives success for discarded data.
func parseContactWrite(app *Node, base collab.Contact) (collab.Contact, error) {
	if app == nil {
		return base, collab.ErrInvalidContact
	}
	base.Emails = append([]collab.Email(nil), base.Emails...)
	base.Phones = append([]collab.Phone(nil), base.Phones...)
	emails := map[byte]int{tokContactEmail1Address: 0, tokContactEmail2Address: 1, tokContactEmail3Address: 2}
	phones := map[byte]string{tokContactHomePhoneNumber: "home", tokContactBusinessPhoneNumber: "work", tokContactMobilePhoneNumber: "mobile"}
	seen := map[string]bool{}
	for _, n := range app.Children {
		key := n.Name()
		if seen[key] {
			return base, collab.ErrInvalidContact
		}
		seen[key] = true
		switch {
		case n.Page == pageContacts:
			if len(n.Children) > 0 || len(n.Opaque) > 0 {
				return base, collab.ErrInvalidContact
			}
			switch n.Token {
			case tokContactFirstName:
				base.GivenName = n.Text
			case tokContactLastName:
				base.FamilyName = n.Text
			case tokContactFileAs:
				base.DisplayName = n.Text
			default:
				if index, ok := emails[n.Token]; ok {
					for len(base.Emails) <= index {
						base.Emails = append(base.Emails, collab.Email{})
					}
					value := strings.TrimSpace(n.Text)
					if value != "" {
						parsed, err := mail.ParseAddress(value)
						if err != nil {
							return base, collab.ErrInvalidContact
						}
						value = parsed.Address
					}
					base.Emails[index].Address = value
				} else if label, ok := phones[n.Token]; ok {
					found := false
					for i := range base.Phones {
						if strings.EqualFold(base.Phones[i].Label, label) {
							base.Phones[i].Number = n.Text
							found = true
							break
						}
					}
					if !found && n.Text != "" {
						base.Phones = append(base.Phones, collab.Phone{Label: label, Number: n.Text})
					}
				} else if n.Text != "" {
					return base, collab.ErrInvalidContact
				}
			}
		case n.Page == pageAirSyncBase && n.Token == tokBaseBody:
			if n.Text != "" || len(n.Opaque) > 0 {
				return base, collab.ErrInvalidContact
			}
			bodySeen := map[byte]bool{}
			for _, v := range n.Children {
				if v.Page != pageAirSyncBase || bodySeen[v.Token] || len(v.Children) > 0 || len(v.Opaque) > 0 {
					return base, collab.ErrInvalidContact
				}
				bodySeen[v.Token] = true
				switch v.Token {
				case tokBaseType:
					if v.Text != "1" {
						return base, collab.ErrInvalidContact
					}
				case tokBaseData:
					base.Note = v.Text
				case tokBaseTruncated:
					if v.Text != "0" && v.Text != "" {
						return base, collab.ErrInvalidContact
					}
				case tokBaseEstimatedDataSize:
				default:
					if v.Text != "" {
						return base, collab.ErrInvalidContact
					}
				}
			}
			if !bodySeen[tokBaseData] {
				base.Note = ""
			}
		case n.Text != "" || len(n.Children) > 0 || len(n.Opaque) > 0:
			return base, collab.ErrInvalidContact
		}
	}
	nextEmails := base.Emails[:0]
	for _, e := range base.Emails {
		if strings.TrimSpace(e.Address) != "" {
			nextEmails = append(nextEmails, e)
		}
	}
	base.Emails = nextEmails
	nextPhones := base.Phones[:0]
	for _, p := range base.Phones {
		if strings.TrimSpace(p.Number) != "" {
			nextPhones = append(nextPhones, p)
		}
	}
	base.Phones = nextPhones
	return collab.NormalizeContact(base)
}

func contactCommandStatus(err error) string {
	switch {
	case err == nil:
		return syncStatusSuccess
	case errors.Is(err, collab.ErrConflict), errors.Is(err, errCollabClientIDConflict):
		return syncStatusConflict
	case errors.Is(err, pgx.ErrNoRows):
		return syncStatusObjectDeleted
	case errors.Is(err, collab.ErrInvalidContact):
		return "6" // invalid client data
	default:
		return syncStatusServerError
	}
}

func (b *SyncBackend) applyContactCommand(ctx context.Context, req *SyncRequest, collection string, bookID int64, cmd *Node, items map[string]itemRow) (string, string) {
	serverID := cmd.ChildText(tokServerID)
	if cmd.Token != tokAdd && cmd.Token != tokChange && cmd.Token != tokDelete {
		return serverID, syncStatusProtocolError
	}
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return serverID, syncStatusServerError
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	svc := appcollab.Service{Q: q}
	principal := appcollab.Principal{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID}
	var version int64
	if cmd.Token == tokAdd {
		if serverID != "" {
			return serverID, syncStatusProtocolError
		}
		contact, parseErr := parseContactWrite(cmd.Child(tokApplicationData), collab.Contact{})
		if parseErr != nil {
			return "", contactCommandStatus(parseErr)
		}
		canonical, _ := json.Marshal(contact)
		result, writeErr := collabAddReceipt(ctx, q, req, collection, "eas_contact_add", cmd.ChildText(tokClientID), canonical, func() (collabAddResult, error) {
			row, e := svc.CreateContact(ctx, principal, bookID, contact)
			return collabAddResult{ServerID: contactServerID(row.ID), Version: row.Version}, e
		})
		if writeErr != nil {
			return "", contactCommandStatus(writeErr)
		}
		serverID, version = result.ServerID, result.Version
	} else {
		id, parseErr := strconv.ParseInt(serverID, 10, 64)
		if parseErr != nil || id <= 0 {
			return serverID, syncStatusObjectDeleted
		}
		row, readErr := q.PlatformGetContact(ctx, store.PlatformGetContactParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, ID: id})
		if readErr != nil {
			return serverID, contactCommandStatus(readErr)
		}
		if row.AddressbookID != bookID {
			return serverID, syncStatusObjectDeleted
		}
		known, ok := items[serverID]
		if !ok || known.Fingerprint != "v"+strconv.FormatInt(row.Version, 10) {
			return serverID, syncStatusConflict
		}
		if cmd.Token == tokDelete {
			_, err = q.PlatformDeleteContact(ctx, store.PlatformDeleteContactParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, ID: id, Version: row.Version})
			if errors.Is(err, pgx.ErrNoRows) {
				err = collab.ErrConflict
			}
		} else {
			contact := collab.Contact{DisplayName: row.DisplayName, GivenName: row.GivenName, FamilyName: row.FamilyName, Note: row.Note}
			if json.Unmarshal(row.Emails, &contact.Emails) != nil || json.Unmarshal(row.Phones, &contact.Phones) != nil {
				return serverID, syncStatusServerError
			}
			contact, err = parseContactWrite(cmd.Child(tokApplicationData), contact)
			if err != nil {
				return serverID, contactCommandStatus(err)
			}
			updated, writeErr := svc.UpdateContact(ctx, principal, id, row.Version, contact)
			err = writeErr
			version = updated.Version
		}
		if err != nil {
			return serverID, contactCommandStatus(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return serverID, syncStatusServerError
	}
	if cmd.Token == tokDelete {
		delete(items, serverID)
	} else {
		items[serverID] = itemRow{ItemID: serverID, Fingerprint: "v" + strconv.FormatInt(version, 10)}
	}
	return serverID, syncStatusSuccess
}

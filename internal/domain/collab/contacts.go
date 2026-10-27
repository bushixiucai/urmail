package collab

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidContact is returned when a contact cannot be stored.
var ErrInvalidContact = errors.New("invalid contact")

// ErrConflict is returned when a write uses a stale object version.
var ErrConflict = errors.New("stale version")

// Email is one address on a contact.
type Email struct {
	Address string `json:"address"`
	Label   string `json:"label,omitempty"`
}

// Phone is one telephone number on a contact.
type Phone struct {
	Number string `json:"number"`
	Label  string `json:"label,omitempty"`
}

// Contact is the mailbox-owned person record.
type Contact struct {
	DisplayName string
	GivenName   string
	FamilyName  string
	Emails      []Email
	Phones      []Phone
	Note        string
}

// NormalizeContact trims fields, lower-cases addresses and rejects unsafe input.
func NormalizeContact(in Contact) (Contact, error) {
	out := Contact{
		DisplayName: cleanLine(in.DisplayName, 200),
		GivenName:   cleanLine(in.GivenName, 100),
		FamilyName:  cleanLine(in.FamilyName, 100),
		Note:        strings.TrimSpace(in.Note),
	}
	if utf8.RuneCountInString(out.Note) > 4000 {
		return Contact{}, ErrInvalidContact
	}
	seen := map[string]struct{}{}
	for _, email := range in.Emails {
		addr := strings.ToLower(strings.TrimSpace(email.Address))
		if !validEmail(addr) {
			return Contact{}, ErrInvalidContact
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out.Emails = append(out.Emails, Email{Address: addr, Label: cleanLine(email.Label, 40)})
	}
	for _, phone := range in.Phones {
		number := cleanLine(phone.Number, 40)
		if number == "" {
			return Contact{}, ErrInvalidContact
		}
		out.Phones = append(out.Phones, Phone{Number: number, Label: cleanLine(phone.Label, 40)})
	}
	if out.DisplayName == "" && out.GivenName == "" && out.FamilyName == "" && len(out.Emails) == 0 {
		return Contact{}, ErrInvalidContact
	}
	if len(out.Emails) > 20 || len(out.Phones) > 20 {
		return Contact{}, ErrInvalidContact
	}
	if out.DisplayName == "" {
		out.DisplayName = strings.TrimSpace(out.GivenName + " " + out.FamilyName)
	}
	if out.DisplayName == "" && len(out.Emails) > 0 {
		out.DisplayName = out.Emails[0].Address
	}
	return out, nil
}

func cleanLine(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if utf8.RuneCountInString(value) > maxRunes {
		return string([]rune(value)[:maxRunes])
	}
	return value
}

func validEmail(addr string) bool {
	if len(addr) < 3 || len(addr) > 320 || strings.ContainsAny(addr, " \t") {
		return false
	}
	at := strings.IndexByte(addr, '@')
	return at > 0 && at < len(addr)-1 && strings.Count(addr, "@") == 1
}

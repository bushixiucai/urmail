package collab

import (
	"strings"
)

// VCard renders one contact as vCard 3.0.
func VCard(in Contact) (string, error) {
	norm, err := NormalizeContact(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
	b.WriteString("FN:" + escapeVCard(norm.DisplayName) + "\r\n")
	b.WriteString("N:" + escapeVCard(norm.FamilyName) + ";" + escapeVCard(norm.GivenName) + ";;;\r\n")
	for _, email := range norm.Emails {
		b.WriteString("EMAIL;TYPE=INTERNET:" + escapeVCard(email.Address) + "\r\n")
	}
	for _, phone := range norm.Phones {
		b.WriteString("TEL:" + escapeVCard(phone.Number) + "\r\n")
	}
	if norm.Note != "" {
		b.WriteString("NOTE:" + escapeVCard(norm.Note) + "\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	return b.String(), nil
}

// ParseVCard reads one vCard 3.0 body and rejects a second card.
func ParseVCard(raw string) (Contact, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	var current Contact
	var done Contact
	cards := 0
	seen := false
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, " ") {
			continue
		}
		switch {
		case strings.EqualFold(line, "BEGIN:VCARD"):
			if seen {
				return Contact{}, ErrInvalidContact
			}
			seen = true
		case strings.EqualFold(line, "END:VCARD"):
			if !seen {
				return Contact{}, ErrInvalidContact
			}
			contact, err := NormalizeContact(current)
			if err != nil {
				return Contact{}, err
			}
			done = contact
			cards++
			seen = false
			current = Contact{}
		case strings.HasPrefix(strings.ToUpper(line), "FN:"):
			current.DisplayName = unescapeVCard(line[3:])
		case strings.HasPrefix(strings.ToUpper(line), "N:"):
			parts := strings.Split(unescapeVCard(line[2:]), ";")
			if len(parts) > 0 {
				current.FamilyName = parts[0]
			}
			if len(parts) > 1 {
				current.GivenName = parts[1]
			}
		case strings.Contains(strings.ToUpper(line), "EMAIL") && strings.Contains(line, ":"):
			value := line[strings.LastIndex(line, ":")+1:]
			current.Emails = append(current.Emails, Email{Address: unescapeVCard(value)})
		case strings.HasPrefix(strings.ToUpper(line), "TEL"):
			value := line[strings.LastIndex(line, ":")+1:]
			current.Phones = append(current.Phones, Phone{Number: unescapeVCard(value)})
		case strings.HasPrefix(strings.ToUpper(line), "NOTE:"):
			current.Note = unescapeVCard(line[5:])
		}
	}
	if cards != 1 {
		return Contact{}, ErrInvalidContact
	}
	return done, nil
}

func escapeVCard(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", ",", "\\,", ";", "\\;")
	return replacer.Replace(value)
}

func unescapeVCard(value string) string {
	replacer := strings.NewReplacer("\\n", "\n", "\\,", ",", "\\;", ";", "\\\\", "\\")
	return replacer.Replace(value)
}

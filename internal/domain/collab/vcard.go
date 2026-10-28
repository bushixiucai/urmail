package collab

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// VCard renders one contact as vCard 3.0.
func VCard(in Contact) (string, error) {
	norm, err := NormalizeContact(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	line := func(value string) { b.WriteString(foldICal(value)); b.WriteString("\r\n") }
	line("BEGIN:VCARD")
	line("VERSION:3.0")
	line("FN:" + escapeVCard(norm.DisplayName))
	line("N:" + escapeVCard(norm.FamilyName) + ";" + escapeVCard(norm.GivenName) + ";;;")
	for _, email := range norm.Emails {
		parameters := ";TYPE=INTERNET"
		if strings.EqualFold(email.Label, "home") || strings.EqualFold(email.Label, "work") {
			parameters += "," + strings.ToUpper(email.Label)
		}
		if email.Label != "" {
			parameters += ";X-URMAIL-LABEL=" + url.QueryEscape(email.Label)
		}
		line("EMAIL" + parameters + ":" + escapeVCard(email.Address))
	}
	for _, phone := range norm.Phones {
		parameters := ""
		if phone.Label != "" {
			parameters = ";X-URMAIL-LABEL=" + url.QueryEscape(phone.Label)
		}
		line("TEL" + parameters + ":" + escapeVCard(phone.Number))
	}
	if norm.Note != "" {
		line("NOTE:" + escapeVCard(norm.Note))
	}
	line("END:VCARD")
	return b.String(), nil
}

// ParseVCard accepts a single UTF-8 vCard 3.0 with folded lines. Unsupported
// legacy transfer encodings are refused instead of importing corrupted text.
func ParseVCard(raw string) (Contact, error) {
	if len(raw) > 200000 || !utf8.ValidString(raw) {
		return Contact{}, ErrInvalidContact
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := []string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
		} else {
			lines = append(lines, line)
		}
	}
	var result Contact
	inside := false
	cards := 0
	for _, line := range lines {
		if strings.EqualFold(line, "BEGIN:VCARD") {
			if inside || cards > 0 {
				return Contact{}, ErrInvalidContact
			}
			inside = true
			continue
		}
		if strings.EqualFold(line, "END:VCARD") {
			if !inside {
				return Contact{}, ErrInvalidContact
			}
			cards++
			inside = false
			continue
		}
		if !inside || line == "" {
			continue
		}
		head, value, ok := strings.Cut(line, ":")
		if !ok {
			return Contact{}, ErrInvalidContact
		}
		parts := strings.Split(head, ";")
		name := strings.ToUpper(parts[0])
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		label := ""
		for _, parameter := range parts[1:] {
			key, v, _ := strings.Cut(parameter, "=")
			switch strings.ToUpper(key) {
			case "ENCODING":
				if !strings.EqualFold(v, "8BIT") {
					return Contact{}, ErrInvalidContact
				}
			case "CHARSET":
				if !strings.EqualFold(v, "UTF-8") {
					return Contact{}, ErrInvalidContact
				}
			case "X-URMAIL-LABEL":
				decoded, err := url.QueryUnescape(v)
				if err != nil {
					return Contact{}, ErrInvalidContact
				}
				label = decoded
			case "TYPE":
				for _, kind := range strings.Split(strings.ToLower(v), ",") {
					if label == "" && (kind == "home" || kind == "work" || kind == "cell" || kind == "mobile") {
						label = kind
					}
				}
			}
		}
		switch name {
		case "VERSION":
			if value != "3.0" {
				return Contact{}, ErrInvalidContact
			}
		case "FN":
			result.DisplayName = unescapeVCard(value)
		case "N":
			values := splitVCardValues(value)
			if len(values) > 0 {
				result.FamilyName = unescapeVCard(values[0])
			}
			if len(values) > 1 {
				result.GivenName = unescapeVCard(values[1])
			}
		case "EMAIL":
			result.Emails = append(result.Emails, Email{Address: unescapeVCard(value), Label: label})
		case "TEL":
			result.Phones = append(result.Phones, Phone{Number: unescapeVCard(value), Label: label})
		case "NOTE":
			result.Note = unescapeVCard(value)
		}
	}
	if cards != 1 || inside {
		return Contact{}, ErrInvalidContact
	}
	return NormalizeContact(result)
}

func splitVCardValues(value string) []string {
	out := []string{}
	start := 0
	escaped := false
	for i, c := range value {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == ';' {
			out = append(out, value[start:i])
			start = i + 1
		}
	}
	return append(out, value[start:])
}

func escapeVCard(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", ",", "\\,", ";", "\\;")
	return replacer.Replace(value)
}

func unescapeVCard(value string) string {
	replacer := strings.NewReplacer("\\n", "\n", "\\,", ",", "\\;", ";", "\\\\", "\\")
	return replacer.Replace(value)
}

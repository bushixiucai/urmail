package collab

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// ParseContactsCSV reads name,email rows. It rejects files over 1000 data rows.
func ParseContactsCSV(raw string) ([]Contact, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	var out []Contact
	first := true
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrInvalidContact
		}
		if first && len(row) >= 2 && strings.EqualFold(row[0], "name") && strings.EqualFold(row[1], "email") {
			first = false
			continue
		}
		first = false
		if len(out) == 1000 {
			return nil, ErrInvalidContact
		}
		if len(row) < 2 {
			return nil, ErrInvalidContact
		}
		contact, err := NormalizeContact(Contact{DisplayName: row[0], Emails: []Email{{Address: row[1]}}})
		if err != nil {
			return nil, err
		}
		out = append(out, contact)
	}
	if len(out) == 0 {
		return nil, ErrInvalidContact
	}
	return out, nil
}

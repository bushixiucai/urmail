// Package aidraft holds user-editable AI draft documents.
package aidraft

import (
	"errors"
	"strings"
)

// ErrInvalidDraft is returned when a draft fails validation.
var ErrInvalidDraft = errors.New("invalid ai draft")

// Draft implements the corresponding domain rule.
type Draft struct {
	Subject, Body string
	Version       int64
}

// Parse implements the corresponding domain rule.
func Parse(subject, body string, version int64) (Draft, error) {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if subject == "" || len(subject) > 200 || strings.ContainsAny(subject, "\r\n") {
		return Draft{}, ErrInvalidDraft
	}
	if body == "" || len(body) > 20000 || version < 1 {
		return Draft{}, ErrInvalidDraft
	}
	return Draft{Subject: subject, Body: body, Version: version}, nil
}

// Edit implements the corresponding domain rule.
func Edit(current, proposed Draft) (Draft, error) {
	if _, err := Parse(current.Subject, current.Body, current.Version); err != nil {
		return Draft{}, err
	}
	if _, err := Parse(proposed.Subject, proposed.Body, proposed.Version); err != nil {
		return Draft{}, err
	}
	if proposed.Version != current.Version+1 {
		return current, nil
	}
	return proposed, nil
}

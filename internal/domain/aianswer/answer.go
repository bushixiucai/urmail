// Package aianswer grounds generated answers in caller-provided mail sources.
package aianswer

import (
	"errors"
	"strings"
)

// ErrUngrounded is returned when an answer cannot cite provided sources.
var ErrUngrounded = errors.New("ungrounded answer")

// ErrInvalidQuestion is returned for empty or malformed questions.
var ErrInvalidQuestion = errors.New("invalid question")

// Source implements the corresponding domain rule.
type Source struct {
	ID    string
	Quote string
}

// Answer implements the corresponding domain rule.
type Answer struct {
	Text      string
	Citations []string
	Grounded  bool
}

// Question implements the corresponding domain rule.
func Question(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	if q == "" || len(q) > 1000 || strings.ContainsAny(q, "\r\n") {
		return "", ErrInvalidQuestion
	}
	return q, nil
}

// SourcesFromSearch implements the corresponding domain rule.
func SourcesFromSearch(ids []string, quotes []string) ([]Source, error) {
	if len(ids) != len(quotes) || len(ids) > 20 {
		return nil, ErrUngrounded
	}
	out := make([]Source, 0, len(ids))
	for i, id := range ids {
		quote := strings.TrimSpace(quotes[i])
		if id == "" || strings.ContainsAny(id, "\r\n ") || quote == "" || len(quote) > 500 {
			return nil, ErrUngrounded
		}
		out = append(out, Source{ID: id, Quote: quote})
	}
	return out, nil
}

// Ground implements the corresponding domain rule.
func Ground(text string, citations []string, sources []Source) (Answer, error) {
	if len(text) == 0 || len(text) > 8000 {
		return Answer{}, ErrUngrounded
	}
	allowed := map[string]struct{}{}
	for _, source := range sources {
		if source.ID == "" || source.Quote == "" {
			return Answer{}, ErrUngrounded
		}
		allowed[source.ID] = struct{}{}
	}
	var kept []string
	seen := map[string]struct{}{}
	for _, citation := range citations {
		if _, ok := allowed[citation]; !ok {
			return Answer{Text: "No authorized mail supports this answer.", Grounded: false}, nil
		}
		if _, ok := seen[citation]; ok {
			continue
		}
		seen[citation] = struct{}{}
		kept = append(kept, citation)
	}
	if len(sources) == 0 || len(kept) == 0 {
		return Answer{Text: "No authorized mail supports this answer.", Grounded: false}, nil
	}
	return Answer{Text: text, Citations: kept, Grounded: true}, nil
}

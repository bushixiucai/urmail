// Package aianswer implements domain rules for aianswer.
package aianswer

import "context"

// Model implements the corresponding domain rule.
type Model func(context.Context, string, []Source) (string, []string, error)

// Ask implements the corresponding domain rule.
func Ask(ctx context.Context, model Model, question string, sources []Source) (Answer, error) {
	q, err := Question(question)
	if err != nil {
		return Answer{}, err
	}
	if model == nil || len(sources) == 0 {
		return Answer{Text: "No authorized mail supports this answer.", Grounded: false}, nil
	}
	text, citations, err := model(ctx, q, sources)
	if err != nil {
		return Answer{}, err
	}
	return Ground(text, citations, sources)
}

// AskMailbox implements the corresponding domain rule.
func AskMailbox(ctx context.Context, model Model, mailboxID int64, question string, sources []ScopedSource) (Answer, error) {
	scoped, err := SourcesForMailbox(mailboxID, sources)
	if err != nil {
		return Answer{}, err
	}
	return Ask(ctx, model, question, scoped)
}

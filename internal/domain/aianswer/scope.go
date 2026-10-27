// Package aianswer implements domain rules for aianswer.
package aianswer

// ScopedSource implements the corresponding domain rule.
type ScopedSource struct {
	MailboxID int64
	Source    Source
}

// SourcesForMailbox implements the corresponding domain rule.
func SourcesForMailbox(mailboxID int64, sources []ScopedSource) ([]Source, error) {
	if mailboxID <= 0 {
		return nil, ErrUngrounded
	}
	out := make([]Source, 0, len(sources))
	for _, source := range sources {
		if source.MailboxID != mailboxID {
			return nil, ErrUngrounded
		}
		out = append(out, source.Source)
	}
	return out, nil
}

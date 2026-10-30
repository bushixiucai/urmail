package mailboxschedule

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func (f *fakeStore) PlatformAcceptMailboxScheduledSend(_ context.Context, p store.PlatformAcceptMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	if f.finishErr != nil {
		return store.MailboxScheduledSends{}, f.finishErr
	}
	if f.row.Status != "submitting" || f.row.ClaimToken != p.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.Status = "sent"
	f.row.SentRawSecret = string(p.SentRaw)
	f.row.SentRaw = nil
	f.row.SentCopyState = "pending"
	f.row.LastError = pgtype.Text{String: "SENT_COPY_PENDING", Valid: true}
	return f.row, nil
}
func (f *fakeStore) PlatformClaimScheduledSentCopy(context.Context) (store.MailboxScheduledSends, error) {
	if f.row.Status != "sent" || len(f.row.SentRawSecret) == 0 || f.row.SentCopyState == "held" {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.ClaimToken++
	return f.row, nil
}
func (f *fakeStore) PlatformMarkScheduledSentCopyUnknown(_ context.Context, p store.PlatformMarkScheduledSentCopyUnknownParams) (store.MailboxScheduledSends, error) {
	if f.row.ClaimToken != p.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.SentCopyState = "unknown"
	return f.row, nil
}
func (f *fakeStore) PlatformCompleteScheduledSentCopy(_ context.Context, p store.PlatformCompleteScheduledSentCopyParams) (store.MailboxScheduledSends, error) {
	if f.row.ClaimToken != p.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.SentCopyState = "complete"
	f.row.SentRaw = nil
	f.row.SentRawSecret = ""
	f.row.SmtpSecret = ""
	f.row.LastError = pgtype.Text{}
	return f.row, nil
}
func (*fakeStore) PlatformDeferScheduledSentCopy(context.Context, store.PlatformDeferScheduledSentCopyParams) error {
	return nil
}
func (f *fakeIMAP) FindSentCopy(context.Context, []byte) (webmail.DraftSaveResult, bool, error) {
	if f.onFind != nil {
		f.onFind()
	}
	return webmail.DraftSaveResult{}, f.copyStored, f.findErr
}

func TestAcceptedScheduledSentCopyRecoveryNeverResubmits(t *testing.T) {
	w, st, imap, submit := workerFixture()
	imap.appendErr = webmail.ErrSentCopyPending
	imap.appendStored = true // Server accepted APPEND; acknowledgement was lost.
	if _, err := w.Step(t.Context()); !errors.Is(err, webmail.ErrSentCopyPending) {
		t.Fatalf("expected pending: %v", err)
	}
	if st.row.Status != "sent" || st.row.SentCopyState != "unknown" || st.row.SentRawSecret == "" || imap.deleted || submit.calls != 1 || imap.appendCalls != 1 {
		t.Fatal("accepted payload not preserved safely")
	}
	imap.appendErr = nil
	if _, err := w.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st.row.SentCopyState != "complete" || len(st.row.SentRaw) != 0 || st.row.SmtpSecret != "" || submit.calls != 1 || imap.appendCalls != 1 || !imap.deleted {
		t.Fatal("lost ACK repair duplicated delivery/copy or retained payload")
	}
}
func TestScheduledSentCopySearchFailureDoesNotBlindlyAppend(t *testing.T) {
	w, st, imap, submit := workerFixture()
	imap.findErr = errors.New("search disconnected")
	if _, err := w.Step(t.Context()); !errors.Is(err, webmail.ErrSentCopyPending) {
		t.Fatal(err)
	}
	if st.row.Status != "sent" || len(st.row.SentRawSecret) == 0 || imap.appendCalls != 0 || imap.deleted || submit.calls != 1 {
		t.Fatal("incomplete reconciliation appended or lost accepted data")
	}
	imap.findErr = nil
	if _, err := w.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if imap.appendCalls != 1 || submit.calls != 1 || st.row.SentCopyState != "complete" {
		t.Fatal("copy-only repair failed")
	}
}

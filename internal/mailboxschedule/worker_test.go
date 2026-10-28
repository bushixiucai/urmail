package mailboxschedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeSecrets struct {
	plain string
	err   error
}

func (f fakeSecrets) DecryptString(string) (string, error) { return f.plain, f.err }

type fakeStore struct {
	row                 store.MailboxScheduledSends
	beginErr, finishErr error
	requeued            bool
}

func (f *fakeStore) PlatformRecoverStaleMailboxScheduledSends(context.Context) (int64, error) {
	return 0, nil
}
func (f *fakeStore) PlatformClaimMailboxScheduledSend(context.Context) (store.MailboxScheduledSends, error) {
	if f.row.Status != "pending" {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.Status = "preparing"
	f.row.Attempts++
	f.row.ClaimToken++
	return f.row, nil
}
func (f *fakeStore) PlatformGetMailboxScheduledSendAddress(context.Context, store.PlatformGetMailboxScheduledSendAddressParams) (string, error) {
	return "alice@example.test", nil
}
func (f *fakeStore) PlatformBeginMailboxScheduledSendSubmission(_ context.Context, p store.PlatformBeginMailboxScheduledSendSubmissionParams) (store.MailboxScheduledSends, error) {
	if f.beginErr != nil {
		return store.MailboxScheduledSends{}, f.beginErr
	}
	if f.row.Status != "preparing" || p.ClaimToken != f.row.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.Status = "submitting"
	return f.row, nil
}
func (f *fakeStore) PlatformFinishMailboxScheduledSend(_ context.Context, p store.PlatformFinishMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	if f.finishErr != nil {
		return store.MailboxScheduledSends{}, f.finishErr
	}
	if f.row.Status != p.ExpectedStatus || p.ClaimToken != f.row.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.Status = p.FinalStatus
	f.row.SmtpSecret = ""
	f.row.LastError = p.LastError
	return f.row, nil
}
func (f *fakeStore) PlatformRequeueMailboxScheduledSend(_ context.Context, p store.PlatformRequeueMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	if f.row.Status != p.ExpectedStatus || p.ClaimToken != f.row.ClaimToken {
		return store.MailboxScheduledSends{}, pgx.ErrNoRows
	}
	f.row.Status = "pending"
	f.row.NextAttemptAt = p.NextAttemptAt
	f.requeued = true
	return f.row, nil
}
func (*fakeStore) CreateAuditLog(context.Context, store.CreateAuditLogParams) (store.AuditLogs, error) {
	return store.AuditLogs{}, nil
}

type fakeIMAP struct {
	copyStored, appendStored bool
	appendCalls              int
	appendErr, findErr       error

	from        []webmail.Address
	validity    uint32
	beforeFetch func(context.Context)
	onDelete    func()
	deleteErr   error
	deleted     bool
	mu          sync.Mutex
}

func (*fakeIMAP) ListFolders(context.Context) ([]webmail.Folder, error) { return nil, nil }
func (*fakeIMAP) ListMessages(context.Context, string, webmail.ListOpts) (webmail.ListResult, error) {
	return webmail.ListResult{}, nil
}
func (f *fakeIMAP) FolderUIDValidity(context.Context, string) (uint32, error) { return f.validity, nil }
func (f *fakeIMAP) GetMessage(ctx context.Context, _ string, uid uint32) (*webmail.Message, error) {
	if f.beforeFetch != nil {
		f.beforeFetch(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid, Subject: "scheduled", From: f.from}, To: []webmail.Address{{Email: "recipient@example.test"}}, Text: "body"}, nil
}
func (*fakeIMAP) GetAttachment(context.Context, string, uint32, string) (string, string, []byte, error) {
	return "", "", nil, errors.New("no attachment")
}
func (*fakeIMAP) SetFlags(context.Context, string, []uint32, []string, []string) error { return nil }
func (*fakeIMAP) MoveMessages(context.Context, string, []uint32, string) error         { return nil }
func (f *fakeIMAP) DeleteMessages(context.Context, string, []uint32, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onDelete != nil {
		f.onDelete()
	}
	f.deleted = true
	return f.deleteErr
}
func (*fakeIMAP) EnsureDraftsFolder(context.Context) (string, error) { return "Drafts", nil }
func (*fakeIMAP) SaveDraft(context.Context, string, webmail.Outgoing, uint32, uint32) (webmail.DraftSaveResult, error) {
	return webmail.DraftSaveResult{}, nil
}
func (f *fakeIMAP) SaveSentCopy(context.Context, []byte) (webmail.DraftSaveResult, error) {
	f.appendCalls++
	if f.appendErr == nil || f.appendStored {
		f.copyStored = true
	}
	return webmail.DraftSaveResult{}, f.appendErr
}
func (*fakeIMAP) MoveToTrash(context.Context, string, []uint32) (string, error) {
	return "Trash", nil
}
func (*fakeIMAP) Close() error { return nil }

type fakeDialer struct{ client webmail.Client }

func (d fakeDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return d.client, nil
}

type fakeSubmitter struct {
	mu            sync.Mutex
	err           error
	calls         int
	lastMessageID string
	before        func()
}

func (s *fakeSubmitter) Send(_ context.Context, _, _ string, msg webmail.Outgoing) (webmail.Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastMessageID = msg.MessageID
	if s.before != nil {
		s.before()
	}
	return webmail.Submission{Raw: []byte("raw")}, s.err
}

func workerFixture() (*Worker, *fakeStore, *fakeIMAP, *fakeSubmitter) {
	st := &fakeStore{row: store.MailboxScheduledSends{ID: 1, TenantID: 1, MailboxID: 1, DraftFolder: "Drafts", DraftUid: 42, DraftUidvalidity: 9, Status: "pending", SmtpSecret: "enc:fixture", SendAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}}
	imap := &fakeIMAP{validity: 9}
	submit := &fakeSubmitter{}
	return NewWorker(Options{Store: st, Secrets: fakeSecrets{plain: "password"}, Dialer: fakeDialer{imap}, Submitter: submit}), st, imap, submit
}

func TestWorkerDeliveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
		retry  bool
	}{
		{"accepted", nil, "sent", false}, {"known rejection", webmail.ErrSubmitFailed, "pending", true},
		{"lost acknowledgement", webmail.ErrSubmitUncertain, "uncertain", false}, {"unclassified outcome", errors.New("unknown outcome"), "uncertain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, imap, submit := workerFixture()
			submit.err = tc.err
			original := st.row.SendAt
			imap.onDelete = func() {
				if st.row.Status != "sent" {
					t.Error("draft deleted before durable acceptance")
				}
			}
			if worked, err := w.Step(context.Background()); err != nil || !worked {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if st.row.Status != tc.status || st.requeued != tc.retry || submit.calls != 1 {
				t.Fatalf("row=%+v retry=%v calls=%d", st.row, st.requeued, submit.calls)
			}
			if submit.lastMessageID != "<urmail-schedule-1@localhost>" {
				t.Fatal("submission lacks stable reconciliation identity")
			}
			if st.row.SendAt != original {
				t.Fatal("original scheduled time changed")
			}
			if tc.status != "pending" && st.row.SmtpSecret != "" {
				t.Fatal("terminal credential retained")
			}
			if tc.status != "sent" && imap.deleted {
				t.Fatal("draft deleted without acceptance")
			}
			if tc.status != "pending" {
				_, _ = w.Step(context.Background())
				if submit.calls != 1 {
					t.Fatal("terminal task resubmitted")
				}
			}
		})
	}
}

func TestWorkerCancellationWinsBeforeSubmission(t *testing.T) {
	w, st, _, submit := workerFixture()
	st.beginErr = pgx.ErrNoRows
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if submit.calls != 0 {
		t.Fatal("canceled claim submitted")
	}
}

func TestWorkerAcceptedFinalizationFailureNeverRequeues(t *testing.T) {
	w, st, imap, submit := workerFixture()
	st.finishErr = errors.New("database unavailable")
	if _, err := w.Step(context.Background()); err == nil {
		t.Fatal("expected persistence error")
	}
	if st.row.Status != "submitting" || st.requeued || imap.deleted || submit.calls != 1 {
		t.Fatalf("status=%s requeued=%v deleted=%v calls=%d", st.row.Status, st.requeued, imap.deleted, submit.calls)
	}
	_, _ = w.Step(context.Background())
	if submit.calls != 1 {
		t.Fatal("accepted submission retried")
	}
}

func TestWorkerAcceptedCleanupFailureRemainsSent(t *testing.T) {
	w, st, imap, _ := workerFixture()
	imap.deleteErr = errors.New("IMAP down")
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.row.Status != "sent" || st.requeued {
		t.Fatal("cleanup failure changed acceptance")
	}
}

func TestWorkerRejectsChangedDraftIdentity(t *testing.T) {
	w, st, imap, submit := workerFixture()
	imap.validity = 10
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.row.Status != "failed" || submit.calls != 0 {
		t.Fatalf("status=%s calls=%d", st.row.Status, submit.calls)
	}
}

func TestWorkerTaskDeadlineStopsPreparation(t *testing.T) {
	w, st, imap, submit := workerFixture()
	w.opts.TaskTimeout = 10 * time.Millisecond
	imap.beforeFetch = func(ctx context.Context) { <-ctx.Done() }
	started := time.Now()
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second || st.row.Status != "pending" || submit.calls != 0 {
		t.Fatal("blocked preparation was not safely bounded")
	}
}

func TestWorkerLostAcceptanceClaimNeverDeletesDraft(t *testing.T) {
	w, st, imap, _ := workerFixture()
	st.finishErr = pgx.ErrNoRows
	if _, err := w.Step(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected lost claim: %v", err)
	}
	if imap.deleted || st.requeued {
		t.Fatal("lost owner cleaned or requeued draft")
	}
}

func TestWorkerChecksDraftIdentityBeforeCleanup(t *testing.T) {
	w, st, imap, submit := workerFixture()
	submit.before = func() { imap.validity = 10 }
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.row.Status != "sent" || imap.deleted {
		t.Fatal("accepted send deleted another mailbox generation")
	}
}

func TestSuspendedScheduleDoesNotSubmit(t *testing.T) {
	w, st, _, submit := workerFixture()
	w.opts.MaySend = func(context.Context, int64, int64) (bool, error) { return false, nil }
	if worked, err := w.Step(context.Background()); err != nil || !worked {
		t.Fatalf("%v %v", worked, err)
	}
	if st.row.Status != "failed" || submit.calls != 0 {
		t.Fatalf("%+v calls=%d", st.row, submit.calls)
	}
}

type aliasGrantStore struct {
	*fakeStore
	allowed int64
}

func (s aliasGrantStore) PlatformMailboxSenderIdentityAllowed(context.Context, store.PlatformMailboxSenderIdentityAllowedParams) (int64, error) {
	return s.allowed, nil
}
func TestWorkerRechecksSavedAliasGrantBeforeSubmission(t *testing.T) {
	for _, allowed := range []int64{0, 1} {
		worker, st, imap, submit := workerFixture()
		imap.from = []webmail.Address{{Name: "Team Desk", Email: "team@example.test"}}
		worker.opts.Store = aliasGrantStore{fakeStore: st, allowed: allowed}
		if _, err := worker.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		if allowed == 0 {
			if submit.calls != 0 || st.row.Status != "failed" || (!st.row.LastError.Valid || st.row.LastError.String != "MAILBOX_FROM_NOT_AUTHORIZED") {
				t.Fatal("revoked draft alias submitted")
			}
		} else if submit.calls != 1 || st.row.Status != "sent" {
			t.Fatal("authorized alias was not sent")
		}
	}
}

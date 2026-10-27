package caldav

import (
	"net/http"
	"strings"
	"testing"
)

func TestDAVRestoreEpochRejectsRewoundCalendarCursor(t *testing.T) {
	f := newPGFixture(t)
	w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/restore.ics", singleICS, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = dav(t, f, f.sessionA, "REPORT", "/caldav/calendar/", syncBody(""), nil)
	old := between(w.Body.String(), "<D:sync-token>", "</D:sync-token>")
	if old == "" {
		t.Fatal("missing cursor")
	}
	// The fixture transaction remains private; set the same restore-generation
	// fact that HoldRestoredSideEffects writes, then run the production handler.
	if _, err := f.tx.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'restore_replay_review','{"sync_epoch":913835}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	w = dav(t, f, f.sessionA, "REPORT", "/caldav/calendar/", syncBody(old), nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "valid-sync-token") {
		t.Fatalf("old cursor accepted: %d %s", w.Code, w.Body.String())
	}
	w = dav(t, f, f.sessionA, "REPORT", "/caldav/calendar/", syncBody(""), nil)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "e913835:") || !strings.Contains(w.Body.String(), "restore.ics") {
		t.Fatalf("full sync failed: %d %s", w.Code, w.Body.String())
	}
	w = dav(t, f, f.sessionB, http.MethodGet, "/caldav/calendar/restore.ics", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("another mailbox read restored event: %d", w.Code)
	}
}

package carddav

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestRestoreRequiresFreshSessionAndFullDAVResync(t *testing.T) {
	f := newCarddavFixture(t)
	ctx := context.Background()
	base := "/api/v1/carddav/" + itoa(f.mailboxA) + "/"
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Retained Contact\r\nEND:VCARD\r\n"
	put := f.do(t, http.MethodPut, base+"retained.vcf", card, nil, true) //nolint:bodyclose // fixture cleanup owns response bodies.
	if put.StatusCode != http.StatusCreated {
		t.Fatal(put.StatusCode)
	}
	resource := put.Header.Get("Location")
	if resource == "" {
		t.Fatal("missing created resource identity")
	}
	report := func(token string) string {
		return `<D:sync-collection xmlns:D="DAV:"><D:sync-token>` + token + `</D:sync-token><D:sync-level>1</D:sync-level></D:sync-collection>`
	}
	response := f.do(t, "REPORT", base, report(""), nil, true) //nolint:bodyclose // fixture cleanup owns response bodies.
	oldToken := tokenFrom(readAll(t, response))
	if oldToken == "" {
		t.Fatal("missing initial token")
	}
	if err := db.HoldRestoredSideEffects(ctx, f.pool.Config().ConnConfig.ConnString(), "carddav-recovery-1"); err != nil {
		t.Fatal(err)
	}
	response = f.do(t, "REPORT", base, report(oldToken), nil, true) //nolint:bodyclose // fixture cleanup owns response bodies.
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("restored session stayed valid: %d", response.StatusCode)
	}
	f.token = "fresh-after-recovery"
	if _, err := f.queries.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: mailboxprincipal.HashToken(f.token), Address: "a@cdv.test"}); err != nil {
		t.Fatal(err)
	}
	response = f.do(t, "REPORT", base, report(oldToken), nil, true) //nolint:bodyclose // fixture cleanup owns response bodies.
	if response.StatusCode != http.StatusForbidden || !strings.Contains(readAll(t, response), "valid-sync-token") {
		t.Fatal("old token did not require full resync")
	}
	response = f.do(t, "REPORT", base, report(""), nil, true) //nolint:bodyclose // fixture cleanup owns response bodies.
	raw := readAll(t, response)
	if response.StatusCode != http.StatusMultiStatus || tokenFrom(raw) == oldToken || !strings.Contains(raw, "200 OK") {
		t.Fatalf("full resync failed: %d %s", response.StatusCode, raw)
	}
	freshToken := tokenFrom(raw)
	updated := strings.Replace(card, "Retained Contact", "Updated After Restore", 1)
	response = f.do(t, http.MethodPut, resource, updated, nil, true) //nolint:bodyclose // fixture owns responses.
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("post-restore update: %d", response.StatusCode)
	}
	response = f.do(t, http.MethodDelete, resource, "", nil, true) //nolint:bodyclose // fixture owns responses.
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("post-restore delete: %d", response.StatusCode)
	}
	response = f.do(t, "REPORT", base, report(freshToken), nil, true) //nolint:bodyclose // fixture owns responses.
	if raw = readAll(t, response); !strings.Contains(raw, "404 Not Found") {
		t.Fatalf("post-restore tombstone absent: %s", raw)
	}
	response = f.do(t, http.MethodGet, resource, "", nil, true) //nolint:bodyclose // fixture owns responses.
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted resource revived: %d", response.StatusCode)
	}
}

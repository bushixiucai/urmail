package caldav

import (
	"net/http"
	"testing"
)

func TestPGConditionalResourceLifecycle(t *testing.T) {
	f := newPGFixture(t)
	target := "/caldav/calendar/condition.ics"
	w := dav(t, f, f.sessionA, http.MethodPut, target, singleICS, map[string]string{"If-None-Match": "*"})
	if w.Code != http.StatusCreated {
		t.Fatalf("conditional create: %d %s", w.Code, w.Body.String())
	}
	tag := w.Header().Get("ETag")
	for _, value := range []string{"*", tag, "W/" + tag, `"different", ` + tag} {
		w = dav(t, f, f.sessionA, http.MethodPut, target, singleICS, map[string]string{"If-None-Match": value})
		if w.Code != http.StatusPreconditionFailed {
			t.Fatalf("If-None-Match %s: %d", value, w.Code)
		}
	}
	w = dav(t, f, f.sessionA, http.MethodPut, target, singleICS, map[string]string{"If-Match": `"stale"`})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update: %d", w.Code)
	}
	w = dav(t, f, f.sessionA, http.MethodPut, target, singleICS, map[string]string{"If-Match": tag, "If-None-Match": `"different"`})
	if w.Code != http.StatusNoContent {
		t.Fatalf("matched update: %d %s", w.Code, w.Body.String())
	}
	tag = w.Header().Get("ETag")
	w = dav(t, f, f.sessionA, http.MethodDelete, target, "", map[string]string{"If-None-Match": tag})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("conditional delete: %d", w.Code)
	}
	w = dav(t, f, f.sessionA, http.MethodDelete, target, "", map[string]string{"If-Match": "*"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("wildcard delete: %d", w.Code)
	}
}

package sqlitedb

import "testing"

// withTxLock must add _txlock=immediate without destroying an existing DSN or
// an in-memory/file URI, and must not duplicate the parameter.
func TestWithTxLock(t *testing.T) {
	cases := map[string]string{
		"/tmp/urmail.db":                  "/tmp/urmail.db?_txlock=immediate",
		"file:urmail.db?cache=shared":     "file:urmail.db?cache=shared&_txlock=immediate",
		":memory:":                        ":memory:?_txlock=immediate",
		"/tmp/urmail.db?_txlock=deferred": "/tmp/urmail.db?_txlock=deferred",
	}
	for in, want := range cases {
		if got := withTxLock(in); got != want {
			t.Errorf("withTxLock(%q) = %q, want %q", in, got, want)
		}
	}
}

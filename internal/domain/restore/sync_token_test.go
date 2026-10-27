package restore

import "testing"

func TestRestoreGenerationRejectsOldAndFutureCursor(t *testing.T) {
	for _, prefix := range []string{"urn:urmail:sync:", "urn:urmail:carddav:"} {
		for _, epoch := range []int64{1, 32548345, 32548346} {
			token := SyncToken(prefix, epoch, 900)
			if cursor, ok := ParseSyncToken(prefix, token, epoch); !ok || cursor != 900 {
				t.Fatal("own token rejected")
			}
			if _, ok := ParseSyncToken(prefix, token, epoch+1); ok {
				t.Fatal("pre-restore token accepted after restore")
			}
		}
		if _, ok := ParseSyncToken(prefix, prefix+"900", 32548345); ok {
			t.Fatal("legacy cursor accepted after rewind")
		}
		if cursor, ok := ParseSyncToken(prefix, "", 32548345); !ok || cursor != 0 {
			t.Fatal("full resync refused")
		}
	}
}

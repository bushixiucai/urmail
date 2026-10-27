package restore

import (
	"strconv"
	"strings"
)

// SyncToken binds a client cursor to the current restore generation. Legacy
// cursors remain valid only on instances that have never been restored.
func SyncToken(prefix string, epoch, cursor int64) string {
	if epoch == 1 {
		return prefix + strconv.FormatInt(cursor, 10)
	}
	return prefix + "e" + strconv.FormatInt(epoch, 10) + ":" + strconv.FormatInt(cursor, 10)
}

// ParseSyncToken rejects pre-restore and foreign-generation cursors instead
// of returning an empty incremental page that would silently lose updates.
func ParseSyncToken(prefix, raw string, epoch int64) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	expected := prefix
	if epoch != 1 {
		expected += "e" + strconv.FormatInt(epoch, 10) + ":"
	}
	if !strings.HasPrefix(raw, expected) {
		return 0, false
	}
	cursor, err := strconv.ParseInt(strings.TrimPrefix(raw, expected), 10, 64)
	return cursor, err == nil && cursor >= 0
}

// Package reconcile assembles and applies desired mail service state.
package reconcile

import "strings"

// withHTTPScheme prefixes a bare host:port address with http:// unless it
// already carries a scheme (nginx.Generate validates backends with
// ^https?://...).
func withHTTPScheme(addr string) string {
	if addr == "" || strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

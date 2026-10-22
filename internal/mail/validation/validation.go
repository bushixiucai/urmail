package validation

import "regexp"

// HostnameRegex validates RFC 1123 hostnames: labels of 1-63 alphanumeric or hyphens,
// cannot start/end with hyphen, total <= 255 characters.
var HostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

// LocalPartRegex validates the local part of an email address per RFC 5321.
var LocalPartRegex = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^` + "`" + `{|}~-]+$`)

// IsValidHostname checks if a hostname complies with RFC 1123.
func IsValidHostname(h string) bool {
	if len(h) == 0 || len(h) > 255 {
		return false
	}
	return HostnameRegex.MatchString(h)
}

// Package validation defines the grammar shared by configuration generators.
package validation

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"
)

// HostnameRegex validates RFC 1123 hostnames: labels of 1-63 alphanumeric or hyphens,
// cannot start/end with hyphen, total <= 255 characters.
var HostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

// LocalPartRegex validates the local part of an email address per RFC 5321.
var LocalPartRegex = regexp.MustCompile("^[a-zA-Z0-9_.!#$%&'*+/=?^`{|}~-]+$")

// DomainNameRegex accepts a DNS name with an optional trailing dot, as it
// appears in zone files.
var DomainNameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}\.?$`)

// IsValidHostname checks if a hostname complies with RFC 1123.
func IsValidHostname(h string) bool {
	if len(h) == 0 || len(h) > 255 {
		return false
	}
	return HostnameRegex.MatchString(h)
}

// IsValidDomainName checks a DNS name that may carry a trailing dot.
func IsValidDomainName(d string) bool {
	if len(d) == 0 || len(d) > 255 {
		return false
	}
	return DomainNameRegex.MatchString(d)
}

// hasWhitespaceOrControl reports whether the string contains whitespace or
// control characters. Such characters would let a value smuggle extra
// directives into line-oriented configuration files.
func hasWhitespaceOrControl(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ValidateAbsolutePath checks that path is an absolute filesystem path free
// of whitespace and control characters.
func ValidateAbsolutePath(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path %q must be absolute", path)
	}
	if hasWhitespaceOrControl(path) {
		return fmt.Errorf("path %q contains whitespace or control characters", path)
	}
	return nil
}

// ValidateSocketPath checks a unix socket reference. It accepts an optional
// "unix:" prefix followed by an absolute path, or a Postfix chroot-relative
// socket name such as "private/policy". Whitespace, control characters and
// traversal are always rejected because sockets are embedded into main.cf /
// master.cf.
func ValidateSocketPath(socket string) error {
	if socket == "" {
		return fmt.Errorf("socket path cannot be empty")
	}
	s := strings.TrimPrefix(socket, "unix:")
	if s == "" {
		return fmt.Errorf("socket path %q has an empty target", socket)
	}
	if hasWhitespaceOrControl(s) {
		return fmt.Errorf("socket path %q contains whitespace or control characters", socket)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("socket path %q must not contain traversal segments", socket)
	}
	return nil
}

// ValidateDatabaseURL checks that a connection string is non-empty and free
// of line breaks or control characters, so it cannot break out of a single
// configuration line in db.conf or main.cf.
func ValidateDatabaseURL(url string) error {
	if url == "" {
		return fmt.Errorf("database URL cannot be empty")
	}
	for _, r := range url {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return fmt.Errorf("database URL contains newline or control characters")
		}
	}
	return nil
}

// IsValidIPv4 reports whether s parses as an IPv4 address.
func IsValidIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// IsValidIPv6 reports whether s parses as an IPv6 address.
func IsValidIPv6(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() == nil
}

package mailguard

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// SourceReason reports how ClientSource derived a client address. Callers
// should log it: it is the operator's evidence for whether a limit was
// applied to the real client or to a proxy hop.
type SourceReason string

const (
	// ReasonPeer means the socket peer was used and no forwarding header
	// was present.
	ReasonPeer SourceReason = "peer"
	// ReasonPeerInvalid means remoteAddr could not be parsed, so no address
	// could be attributed.
	ReasonPeerInvalid SourceReason = "peer-invalid"
	// ReasonPeerForwardedIgnored means a forwarding header was present but
	// the socket peer is not a trusted proxy, so the header was ignored.
	ReasonPeerForwardedIgnored SourceReason = "peer-forwarded-ignored"
	// ReasonTrustedPeerNoForwarded means the peer is a trusted proxy but no
	// X-Forwarded-For header was sent, so the proxy itself was attributed.
	ReasonTrustedPeerNoForwarded SourceReason = "trusted-peer-no-forwarded"
	// ReasonForwarded means the rightmost untrusted hop of X-Forwarded-For
	// was attributed.
	ReasonForwarded SourceReason = "forwarded"
	// ReasonForwardedMalformed means X-Forwarded-For contained a value that
	// is not a plain IP address, so the peer was attributed instead.
	ReasonForwardedMalformed SourceReason = "forwarded-malformed"
	// ReasonForwardedNotAllowed means X-Forwarded-For named an address that
	// may not be a public client (loopback, private, link-local, multicast
	// or unspecified), so the peer was attributed instead.
	ReasonForwardedNotAllowed SourceReason = "forwarded-not-allowed"
	// ReasonForwardedAllTrusted means every X-Forwarded-For hop is itself a
	// configured trusted proxy, so the peer was attributed.
	ReasonForwardedAllTrusted SourceReason = "forwarded-all-trusted"
)

// ClientSource returns the address that abuse controls must attribute a
// request or connection to.
//
// The socket peer (remoteAddr, host or host:port) is authoritative. It is
// only replaced by X-Forwarded-For when the peer itself is covered by one of
// trustedProxies. The forwarding chain is then walked from the RIGHT, which
// is the only end a proxy appends to: trusted proxies are skipped and the
// first untrusted address is returned. Values further left are
// client-controlled and never trusted. Anything malformed, or any
// non-public address that is not itself a trusted proxy, fails closed to the
// socket peer.
//
// IPv4-mapped IPv6 addresses are normalized to IPv4 and IPv6 zones are
// dropped, so one client always maps to one limiter bucket.
func ClientSource(remoteAddr string, headers http.Header, trustedProxies []netip.Prefix) (netip.Addr, SourceReason) {
	peer, ok := parsePeer(remoteAddr)
	if !ok {
		return netip.Addr{}, ReasonPeerInvalid
	}

	hops, hasForwarded := forwardedHops(headers)
	if !isTrustedProxy(peer, trustedProxies) {
		if hasForwarded {
			return peer, ReasonPeerForwardedIgnored
		}
		return peer, ReasonPeer
	}
	if len(hops) == 0 {
		return peer, ReasonTrustedPeerNoForwarded
	}

	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseForwardedHop(hops[i])
		if !ok {
			return peer, ReasonForwardedMalformed
		}
		if isTrustedProxy(hop, trustedProxies) {
			continue
		}
		if !usableSource(hop) {
			return peer, ReasonForwardedNotAllowed
		}
		return hop, ReasonForwarded
	}
	return peer, ReasonForwardedAllTrusted
}

// ParseTrustedProxies parses a comma or whitespace separated list of CIDR
// prefixes or bare IP addresses into normalized prefixes. Bare addresses
// become a single-host prefix so a deployment cannot accidentally trust an
// entire address family.
func ParseTrustedProxies(values ...string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		for _, field := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			prefix, err := parseTrustedProxy(field)
			if err != nil {
				return nil, err
			}
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes, nil
}

func parseTrustedProxy(field string) (netip.Prefix, error) {
	if strings.Contains(field, "/") {
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("mailguard: invalid trusted proxy %q: %w", field, err)
		}
		normalized, ok := normalizePrefix(prefix)
		if !ok {
			return netip.Prefix{}, fmt.Errorf("mailguard: invalid trusted proxy %q", field)
		}
		return normalized, nil
	}

	addr, err := netip.ParseAddr(field)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("mailguard: invalid trusted proxy %q: %w", field, err)
	}
	addr = normalizeAddr(addr)
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// forwardedHops returns the X-Forwarded-For entries in wire order (left to
// right) and whether the header was present at all. Every header line is
// included because a chain of proxies may append separate lines.
func forwardedHops(headers http.Header) ([]string, bool) {
	values := headers.Values("X-Forwarded-For")
	if len(values) == 0 {
		return nil, false
	}
	hops := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			hops = append(hops, strings.TrimSpace(part))
		}
	}
	return hops, true
}

func parsePeer(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return normalizeAddr(addr), true
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return normalizeAddr(addrPort.Addr()), true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		if addr, err := netip.ParseAddr(value[1 : len(value)-1]); err == nil {
			return normalizeAddr(addr), true
		}
	}
	return netip.Addr{}, false
}

// parseForwardedHop accepts only a plain IPv4/IPv6 address (optionally
// bracketed). Ports, quotes, RFC 7239 "for=" parameters and host names are
// treated as malformed: a proxy that emits them must be fixed rather than
// guessed around.
func parseForwardedHop(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "=;\"'") {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return normalizeAddr(addr), true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		if addr, err := netip.ParseAddr(value[1 : len(value)-1]); err == nil {
			return normalizeAddr(addr), true
		}
	}
	return netip.Addr{}, false
}

// usableSource rejects addresses that cannot identify a public client. A
// forwarded private address is deliberately not accepted: it is ambiguous
// (many networks reuse the same ranges) and could let an internal hop's
// clients share a ban bucket by accident.
func usableSource(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	return !addr.IsUnspecified() &&
		!addr.IsLoopback() &&
		!addr.IsPrivate() &&
		!addr.IsMulticast() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() &&
		!addr.IsInterfaceLocalMulticast()
}

func normalizeAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// normalizePrefix turns an IPv4-mapped IPv6 prefix into its IPv4
// equivalent, because netip treats the two families as distinct when
// matching.
func normalizePrefix(prefix netip.Prefix) (netip.Prefix, bool) {
	if !prefix.IsValid() {
		return netip.Prefix{}, false
	}
	addr := prefix.Addr()
	if !addr.Is4In6() {
		return prefix.Masked(), true
	}
	bits := prefix.Bits() - 96
	if bits < 0 || bits > 32 {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr.Unmap(), bits).Masked(), true
}

func isTrustedProxy(addr netip.Addr, prefixes []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	addr = normalizeAddr(addr)
	for _, prefix := range prefixes {
		normalized, ok := normalizePrefix(prefix)
		if !ok {
			continue
		}
		if normalized.Contains(addr) {
			return true
		}
	}
	return false
}

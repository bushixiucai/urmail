package certs

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"
)

// LoopbackCertificatePolicy describes how a client treats the certificate of a
// loopback mail listener (webmail -> Dovecot IMAPS).
//
// The only supported certificate exemption in this product is the loopback one:
// Dovecot serves the public mail hostname certificate on 127.0.0.1, where the
// dial address cannot match the certificate name. Every other destination must
// verify the chain and the name against the system roots.
type LoopbackCertificatePolicy struct {
	// ServerName is the identity expected from the loopback listener. Empty
	// means "use the dial host".
	ServerName string
	// SkipVerification exempts a *loopback* listener from chain and name
	// verification. It is ignored for any non-loopback destination.
	SkipVerification bool
}

// LoopbackTLSConfig builds the client TLS configuration for one mail endpoint.
//
// The exemption is scoped structurally, not by convention: a non-loopback
// destination always gets full verification with the dial host as ServerName,
// whatever the policy asks for. Callers therefore cannot turn the loopback
// exception into "skip verification everywhere" by misconfiguring one field.
func LoopbackTLSConfig(address string, policy LoopbackCertificatePolicy) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" {
		return nil, errors.New("mail endpoint address must be host:port")
	}
	serverName := policy.ServerName
	if serverName == "" {
		serverName = host
	}
	if !IsLoopbackHost(host) {
		return &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}, nil
	}
	return &tls.Config{
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: policy.SkipVerification, // #nosec G402 -- loopback-only exemption, scoped by IsLoopbackHost above.
	}, nil
}

// IsLoopbackHost reports whether host is a loopback destination. Only an IP
// literal inside 127.0.0.0/8 or ::1, or the literal name "localhost", qualifies;
// a resolvable public name does not, so DNS rebinding cannot widen the
// exemption.
func IsLoopbackHost(host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(trimmed, "localhost") {
		return true
	}
	ip := net.ParseIP(trimmed)
	return ip != nil && ip.IsLoopback()
}

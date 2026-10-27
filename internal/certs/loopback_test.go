package certs

import (
	"crypto/tls"
	"testing"
)

// TestLoopbackTLSConfigScopesExemption covers MAIL-006: the loopback
// certificate exception is scoped structurally. A caller that sets
// SkipVerification can never widen it past 127.0.0.1/::1 — a non-loopback
// destination always gets full verification.
func TestLoopbackTLSConfigScopesExemption(t *testing.T) {
	policy := LoopbackCertificatePolicy{SkipVerification: true}

	cfg, err := LoopbackTLSConfig("127.0.0.1:993", policy)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("loopback destination must honor the exemption")
	}

	cfg, err = LoopbackTLSConfig("[::1]:993", policy)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("IPv6 loopback destination must honor the exemption")
	}

	// Same policy, public address: verification must stay on and ServerName
	// must be the dial host.
	cfg, err = LoopbackTLSConfig("mail.example.com:993", policy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InsecureSkipVerify {
		t.Error("non-loopback destination must never be exempt from verification")
	}
	if cfg.ServerName != "mail.example.com" {
		t.Errorf("ServerName = %q, want the dial host", cfg.ServerName)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %v, want TLS 1.2", cfg.MinVersion)
	}
}

func TestLoopbackTLSConfigServerNameOverride(t *testing.T) {
	policy := LoopbackCertificatePolicy{ServerName: "box.urmail.cn", SkipVerification: true}
	cfg, err := LoopbackTLSConfig("127.0.0.1:993", policy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "box.urmail.cn" {
		t.Errorf("ServerName = %q, want the configured mail hostname", cfg.ServerName)
	}
	// The override also applies on the non-loopback path and must never enable
	// skipping there.
	cfg, err = LoopbackTLSConfig("imap.example.net:993", policy)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "box.urmail.cn" {
		t.Errorf("ServerName = %q, want the configured identity", cfg.ServerName)
	}
	if cfg.InsecureSkipVerify {
		t.Error("non-loopback destination must verify the chain")
	}
}

func TestLoopbackTLSConfigRejectsBadAddress(t *testing.T) {
	if _, err := LoopbackTLSConfig("no-port", LoopbackCertificatePolicy{}); err == nil {
		t.Fatal("address without port must be rejected")
	}
	if _, err := LoopbackTLSConfig("   ", LoopbackCertificatePolicy{}); err == nil {
		t.Fatal("empty address must be rejected")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "127.9.9.9"} {
		if !IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"mail.example.com", "127.0.0.1.example.com", "fe80::1", "0.0.0.0"} {
		if IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", host)
		}
	}
}

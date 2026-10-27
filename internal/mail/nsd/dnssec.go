package nsd

import (
	"fmt"
	"sort"
	"strings"
)

// DNSSEC algorithm numbers (RFC 8624).
const (
	AlgorithmRSASHA256       uint8 = 8
	AlgorithmRSASHA512       uint8 = 10
	AlgorithmECDSAP256SHA256 uint8 = 13
)

// DNSSEC key flags: 256 = ZSK, 257 = KSK (RFC 4034).
const (
	FlagZSK uint16 = 256
	FlagKSK uint16 = 257
)

// DNSSECKeyState is the lifecycle state of one signing key in the rendered
// snapshot. The states form the linear chain
//
//	generated -> published -> signing -> retiring -> removed
//
// enforced by TransitionKeyState in rollover.go. Only "published", "signing"
// and "retiring" keys are rendered into signconf; "generated" keys exist only
// in the key store and must not be advertised in DNS yet.
type DNSSECKeyState string

// DNSSEC key lifecycle states, see DNSSECKeyState.
const (
	KeyStateGenerated DNSSECKeyState = "generated"
	KeyStatePublished DNSSECKeyState = "published"
	KeyStateSigning   DNSSECKeyState = "signing"
	KeyStateRetiring  DNSSECKeyState = "retiring"
	KeyStateRemoved   DNSSECKeyState = "removed"
)

// DNSSECKey is one signing key in the zone key snapshot.
type DNSSECKey struct {
	KeyTag      uint16         // RFC 4034 key tag
	Flags       uint16         // 256 (ZSK) or 257 (KSK/CSK)
	Algorithm   uint8          // AlgorithmRSASHA256, ... ECDSAP256SHA256
	PublicKey   string         // base64 DNSKEY public key material
	DSRecord    string         // Delegation Signer data (key tag, flags, algorithm, digest), when computed
	State       DNSSECKeyState // lifecycle state, see DNSSECKeyState
	CreatedAt   string         // RFC 3339 UTC, caller-provided so renders stay deterministic
	PublishedAt string         // RFC 3339 UTC, when the DNSKEY was first published ("" if not yet)
}

// DefaultRolloverWaitTTL is the default DNSKEY propagation wait used by the
// rollover planner when the caller does not override it.
const DefaultRolloverWaitTTL = 86400

// DNSSECConfig marks a zone as signed and carries the key snapshot.
type DNSSECConfig struct {
	// KeyDirectory is the per-zone directory holding private key material,
	// e.g. "/var/lib/nsd/keys/example.com". It must be outside the rendered
	// zone/config trees and readable only by the nsd user.
	KeyDirectory string
	Keys         []DNSSECKey
	// RolloverWaitTTL is the DNSKEY TTL wait between rollover steps.
	// 0 selects DefaultRolloverWaitTTL.
	RolloverWaitTTL int
	// SignatureValidity configures nsd signature lifetimes (seconds).
	// 0 selects the defaults below.
	SignatureValidity int
	SignatureRefresh  int
}

const (
	defaultSignatureValidity = 30 * 24 * 3600
	defaultSignatureRefresh  = 5 * 24 * 3600
)

// normalized fills defaults and validates the signing block.
func (d *DNSSECConfig) normalized(domain string) (DNSSECConfig, error) {
	if strings.TrimSpace(d.KeyDirectory) == "" {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: DNSSEC key directory is required", ErrInvalidZone, domain)
	}
	if d.KeyDirectory != cleanPathPosix(d.KeyDirectory) {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: key directory %q is not a clean path", ErrInvalidZone, domain, d.KeyDirectory)
	}
	if !strings.HasPrefix(d.KeyDirectory, "/") {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: key directory %q must be absolute", ErrInvalidZone, domain, d.KeyDirectory)
	}
	out := *d
	if out.RolloverWaitTTL == 0 {
		out.RolloverWaitTTL = DefaultRolloverWaitTTL
	}
	if out.SignatureValidity == 0 {
		out.SignatureValidity = defaultSignatureValidity
	}
	if out.SignatureRefresh == 0 {
		out.SignatureRefresh = defaultSignatureRefresh
	}
	if out.SignatureRefresh >= out.SignatureValidity {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: signature refresh must be shorter than validity", ErrInvalidZone, domain)
	}
	if len(out.Keys) == 0 {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: signed zone needs at least one DNSSEC key", ErrInvalidZone, domain)
	}
	tags := make(map[uint16]bool, len(out.Keys))
	signing := 0
	for _, k := range out.Keys {
		if tags[k.KeyTag] {
			return DNSSECConfig{}, fmt.Errorf("%w: zone %s: duplicate DNSSEC key tag %d", ErrInvalidZone, domain, k.KeyTag)
		}
		tags[k.KeyTag] = true
		if k.Flags != FlagZSK && k.Flags != FlagKSK {
			return DNSSECConfig{}, fmt.Errorf("%w: zone %s: DNSSEC key flags must be %d or %d, got %d", ErrInvalidZone, domain, FlagZSK, FlagKSK, k.Flags)
		}
		switch k.Algorithm {
		case AlgorithmRSASHA256, AlgorithmRSASHA512, AlgorithmECDSAP256SHA256:
		default:
			return DNSSECConfig{}, fmt.Errorf("%w: zone %s: unsupported DNSSEC algorithm %d", ErrInvalidZone, domain, k.Algorithm)
		}
		if strings.TrimSpace(k.PublicKey) == "" {
			return DNSSECConfig{}, fmt.Errorf("%w: zone %s: DNSSEC key %d has empty public key material", ErrInvalidZone, domain, k.KeyTag)
		}
		switch k.State {
		case KeyStateGenerated, KeyStatePublished, KeyStateSigning, KeyStateRetiring, KeyStateRemoved:
		default:
			return DNSSECConfig{}, fmt.Errorf("%w: zone %s: unknown DNSSEC key state %q", ErrInvalidZone, domain, k.State)
		}
		if k.State == KeyStateSigning {
			signing++
		}
	}
	if signing == 0 {
		return DNSSECConfig{}, fmt.Errorf("%w: zone %s: signed zone needs at least one key in signing state", ErrInvalidZone, domain)
	}
	return out, nil
}

func sortedKeys(keys []DNSSECKey) []DNSSECKey {
	out := make([]DNSSECKey, len(keys))
	copy(out, keys)
	sort.Slice(out, func(i, j int) bool { return out[i].KeyTag < out[j].KeyTag })
	return out
}

// renderSignConf renders the per-zone signing configuration consumed by the
// apply layer (installed as /etc/nsd/nsd.conf.d/signconf/<zone>.conf). It is
// pure: key material is already in the snapshot, nothing is executed.
func renderSignConf(domain string, d DNSSECConfig, serial uint32) string {
	var b strings.Builder
	b.WriteString("# Generated by urmaild. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "# Signing configuration for %s\n", domain)
	b.WriteString("zone:\n")
	fmt.Fprintf(&b, "  name: %q\n", domain)
	fmt.Fprintf(&b, "  key-directory: %q\n", d.KeyDirectory)
	fmt.Fprintf(&b, "  sig-signing-signatures: %d\n", d.SignatureValidity)
	fmt.Fprintf(&b, "  sig-signing-signature: %d\n", d.SignatureRefresh)
	fmt.Fprintf(&b, "  # SOA serial at last render: %d\n", serial)
	b.WriteString("\n")
	for _, k := range sortedKeys(d.Keys) {
		b.WriteString("key:\n")
		fmt.Fprintf(&b, "  tag: %d\n", k.KeyTag)
		fmt.Fprintf(&b, "  flags: %d\n", k.Flags)
		fmt.Fprintf(&b, "  algorithm: %d\n", k.Algorithm)
		fmt.Fprintf(&b, "  state: %s\n", k.State)
		fmt.Fprintf(&b, "  public-key: %q\n", k.PublicKey)
		if k.DSRecord != "" {
			fmt.Fprintf(&b, "  ds: %q\n", k.DSRecord)
		}
		fmt.Fprintf(&b, "  created-at: %q\n", k.CreatedAt)
		if k.PublishedAt != "" {
			fmt.Fprintf(&b, "  published-at: %q\n", k.PublishedAt)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderKeyMetadata renders the rollover metadata sidecar (installed as
// /var/lib/nsd/keys/<zone>/rollover.state) recording serial/key states so the
// operator and the reconcile loop can detect a half-finished rollover.
func renderKeyMetadata(domain string, d DNSSECConfig, serial uint32) string {
	var b strings.Builder
	b.WriteString("# Generated by urmaild. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "zone: %s\n", domain)
	fmt.Fprintf(&b, "serial: %d\n", serial)
	fmt.Fprintf(&b, "rollover_wait_ttl: %d\n", d.RolloverWaitTTL)
	for _, k := range sortedKeys(d.Keys) {
		fmt.Fprintf(&b, "key %d: flags=%d algorithm=%d state=%s created_at=%s published_at=%s\n",
			k.KeyTag, k.Flags, k.Algorithm, k.State, k.CreatedAt, k.PublishedAt)
	}
	return b.String()
}

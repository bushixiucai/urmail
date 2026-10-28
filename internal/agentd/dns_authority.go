package agentd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// OpDNSAuthorityManage names the bounded authoritative DNS lifecycle operation.
const OpDNSAuthorityManage = "dns.authority.manage"

// OpDNSAuthorityStatus names the bounded live DNS evidence operation.
const OpDNSAuthorityStatus = "dns.authority.status"
const dnsKeyTTL = 86400
const dnsTSIGPath = "/etc/nsd/nsd.conf.d/urmail-tsig.keys"

// DNSAuthorityParams deliberately contains no paths, commands or private keys.
type DNSAuthorityParams struct {
	ActorID   int64                `json:"actor_id,omitempty"`
	TenantID  int64                `json:"tenant_id,omitempty"`
	DomainID  int64                `json:"domain_id,omitempty"`
	Secondary *nsd.SecondaryConfig `json:"secondary,omitempty"`
	Domain    string               `json:"domain"`
	Action    string               `json:"action"`
}
type dnsKeyGeneration struct {
	Keys      []string  `json:"keys"`
	CreatedAt time.Time `json:"created_at"`
}
type dnsKeyState struct {
	Current       dnsKeyGeneration  `json:"current"`
	Previous      *dnsKeyGeneration `json:"previous,omitempty"`
	SigningEpoch  time.Time         `json:"signing_epoch"`
	PublishedAt   time.Time         `json:"published_at"`
	Phase         string            `json:"phase"`
	TSIGName      string            `json:"tsig_name,omitempty"`
	ParentDSTTL   int               `json:"parent_ds_ttl"`
	DSAbsentSince time.Time         `json:"ds_absent_since"`
	TSIGSecret    string            `json:"tsig_secret,omitempty"`
}

// DNSAuthorityStatus exposes public evidence without key or TSIG material.
type DNSAuthorityStatus struct {
	Domain                 string               `json:"domain"`
	Phase                  string               `json:"phase"`
	LocalSignaturesValid   bool                 `json:"local_signatures_valid"`
	ParentDSChecked        bool                 `json:"parent_ds_checked"`
	ParentDSMatches        bool                 `json:"parent_ds_matches"`
	ChainValid             bool                 `json:"chain_valid"`
	ParentDSTTL            int                  `json:"parent_ds_ttl"`
	ParentAbsenceValidated bool                 `json:"parent_absence_validated"`
	Enabled                bool                 `json:"enabled"`
	SecondaryAnswering     bool                 `json:"secondary_answering"`
	SecondarySOAMatches    bool                 `json:"secondary_soa_matches"`
	PrimarySOA             string               `json:"primary_soa,omitempty"`
	SecondarySOA           string               `json:"secondary_soa,omitempty"`
	Secondary              *nsd.SecondaryConfig `json:"secondary,omitempty"`
	CanDisable             bool                 `json:"can_disable"`
	Ready                  bool                 `json:"ready"`
	DS                     []string             `json:"ds"`
	ParentDS               []string             `json:"parent_ds"`
	SignaturesExpireAt     time.Time            `json:"signatures_expire_at"`
	RolloverNotBefore      time.Time            `json:"rollover_not_before"`
}

// RegisterDNSAuthorityOperations adds only typed DNS authority capabilities.
func RegisterDNSAuthorityOperations(r *Registry, m *ApplyManager) {
	r.Register(OpDNSAuthorityManage, m.ManageDNSAuthority)
	r.Register(OpDNSAuthorityStatus, m.DNSAuthorityStatus)
}

type dnsToolBuffer struct{ bytes.Buffer }

func (b *dnsToolBuffer) Write(data []byte) (int, error) {
	const limit = 2 << 20
	if len(data) > limit-b.Len() {
		return 0, errors.New("DNS tool output exceeds limit")
	}
	return b.Buffer.Write(data)
}
func dnsKeyName(domain, key string) bool {
	return dnsDomain(domain) && regexp.MustCompile(`^K`+regexp.QuoteMeta(domain)+`\.\+013\+[0-9]{5}$`).MatchString(key)
}

func (m *ApplyManager) dnsRoot() string                   { return filepath.Join(m.root, "dns-security") }
func (m *ApplyManager) dnsDirectory(domain string) string { return filepath.Join(m.dnsRoot(), domain) }
func dnsDomain(domain string) bool {
	return domain == strings.ToLower(domain) && len(domain) <= 253 && validation.IsValidHostname(domain) && !strings.HasPrefix(domain, "-")
}
func (m *ApplyManager) dnsOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.output != nil {
		return m.output(ctx, append([]string{name}, args...)...)
	}
	switch name {
	case "/usr/bin/dig", "/usr/bin/delv", "/usr/bin/dnssec-keygen", "/usr/bin/dnssec-signzone", "/usr/bin/dnssec-verify", "/usr/bin/dnssec-dsfromkey", "/usr/sbin/nsd-checkzone", "/usr/sbin/nsd-checkconf":
	default:
		return nil, fmt.Errorf("unlisted DNS executable")
	}
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // Absolute executable whitelist above; arguments are structured and never interpreted by a shell.
	var bounded dnsToolBuffer
	command.Stdout = &bounded
	command.Stderr = &bounded
	err := command.Run()
	output := bounded.Bytes()
	if err != nil {
		return nil, fmt.Errorf("DNS tool %s failed", filepath.Base(name))
	}
	if len(output) > 2<<20 {
		return nil, fmt.Errorf("DNS tool output too large")
	}
	return output, nil
}
func protectedDNSDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("unsafe DNS private directory")
	}
	return nil
}
func (m *ApplyManager) loadDNSState(domain string) (dnsKeyState, error) {
	var state dnsKeyState
	path := filepath.Join(m.dnsDirectory(domain), "state.json")
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	uid, _ := fileOwner(info)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || uid != os.Geteuid() || info.Size() > 65536 {
		return state, fmt.Errorf("unsafe DNS key state")
	}
	data, err := os.ReadFile(path) //nolint:gosec // Fixed private state basename under the validated root-owned zone directory.
	if err != nil {
		return state, err
	}
	if len(data) > 65536 {
		return state, fmt.Errorf("DNS key state too large")
	}
	err = json.Unmarshal(data, &state)
	return state, err
}
func (m *ApplyManager) saveDNSState(domain string, state dnsKeyState) error {
	directory := m.dnsDirectory(domain)
	if err := protectedDNSDirectory(m.dnsRoot()); err != nil {
		return err
	}
	if err := protectedDNSDirectory(directory); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeFileAtomic(filepath.Join(directory, "state.json"), data, 0600, []string{m.dnsRoot()})
	return err
}
func (m *ApplyManager) authorizedDNS(ctx context.Context, domain string, tenantID, domainID, actorID int64) (store.PlatformGetDNSSecurityRow, error) {
	if m.cfg == nil || m.cfg.DNSSecondaryOnly || m.cfg.PreserveExternalNSD || m.pool == nil {
		return store.PlatformGetDNSSecurityRow{}, fmt.Errorf("%w: authoritative DNS management is unavailable", ErrRejected)
	}
	row, err := store.New(m.pool.Pool).PlatformGetDNSSecurity(ctx, domain)
	if err != nil {
		return row, err
	}
	if (tenantID != 0 || domainID != 0) && (row.OwnerTenantID != tenantID || row.RegisteredDomainID != domainID) {
		return row, fmt.Errorf("%w: DNS zone ownership changed", ErrRejected)
	}
	if actorID != 0 {
		role, roleErr := store.New(m.pool).GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{UserID: actorID, TenantID: row.OwnerTenantID})
		if roleErr != nil || (role != "owner" && role != "admin") {
			return row, fmt.Errorf("%w: DNS authority role revoked", ErrRejected)
		}
	}
	return row, nil
}
func (m *ApplyManager) prepareDNSKeys(ctx context.Context, domain string) (dnsKeyGeneration, error) {
	directory := m.dnsDirectory(domain)
	if err := protectedDNSDirectory(m.dnsRoot()); err != nil {
		return dnsKeyGeneration{}, err
	}
	if err := protectedDNSDirectory(directory); err != nil {
		return dnsKeyGeneration{}, err
	}
	generation := dnsKeyGeneration{CreatedAt: time.Now().UTC()}
	for _, role := range []string{"KSK", "ZSK"} {
		args := []string{"-q", "-a", "ECDSAP256SHA256", "-n", "ZONE", "-K", directory, "-L", "86400"}
		if role == "KSK" {
			args = append(args, "-f", "KSK")
		}
		args = append(args, domain)
		output, err := m.dnsOutput(ctx, "/usr/bin/dnssec-keygen", args...)
		if err != nil {
			return generation, err
		}
		key := strings.TrimSpace(string(output))
		if !regexp.MustCompile(`^K` + regexp.QuoteMeta(domain) + `\.\+013\+[0-9]{5}$`).MatchString(key) {
			return generation, fmt.Errorf("invalid generated DNS key name")
		}
		for _, suffix := range []string{".key", ".private"} {
			path := filepath.Join(directory, key+suffix)
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return generation, fmt.Errorf("DNS key not a regular file")
			}
			if err = os.Chmod(path, 0600); err != nil {
				return generation, err
			}
		}
		generation.Keys = append(generation.Keys, key)
	}
	return generation, nil
}
func (m *ApplyManager) dnsDS(ctx context.Context, domain string, generation dnsKeyGeneration) ([]string, error) {
	var records []string
	for _, key := range generation.Keys {
		if !dnsKeyName(domain, key) {
			return nil, fmt.Errorf("invalid stored DNS key reference")
		}
		raw, err := os.ReadFile(filepath.Join(m.dnsDirectory(domain), key+".key")) //nolint:gosec // Both zone and canonical BIND key basename were validated; root-private directory.
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(raw), "DNSKEY 257 ") {
			continue
		}
		data, err := m.dnsOutput(ctx, "/usr/bin/dnssec-dsfromkey", "-2", filepath.Join(m.dnsDirectory(domain), key+".key"))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				records = append(records, strings.Join(strings.Fields(line), " "))
			}
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("KSK DS record unavailable")
	}
	return records, nil
}
func (m *ApplyManager) dnsDSFromStoredGeneration(ctx context.Context, domain string, generation dnsKeyGeneration) ([]string, error) {
	var records []string
	for _, key := range generation.Keys {
		if !regexp.MustCompile(`^K` + regexp.QuoteMeta(domain) + `\.\+013\+[0-9]{5}$`).MatchString(key) {
			return nil, fmt.Errorf("invalid stored DNS key reference")
		}
		path := filepath.Join(m.dnsDirectory(domain), key+".key")
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			path = filepath.Join(m.dnsArchiveDirectory(domain, generation), key+".key")
		}
		raw, err := os.ReadFile(path) //nolint:gosec // Canonical key reference validated above, restricted to live or retired private zone namespace.
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(raw), "DNSKEY 257 ") {
			continue
		}
		data, err := m.dnsOutput(ctx, "/usr/bin/dnssec-dsfromkey", "-2", path)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				records = append(records, strings.Join(strings.Fields(line), " "))
			}
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("stored KSK DS unavailable")
	}
	return records, nil
}

func dsValue(line string) string {
	fields := strings.Fields(line)
	for i, field := range fields {
		if strings.EqualFold(field, "DS") {
			fields = fields[i+1:]
			break
		}
	}
	if len(fields) < 4 {
		return ""
	}
	tag, e1 := strconv.ParseUint(fields[0], 10, 16)
	algorithm, e2 := strconv.ParseUint(fields[1], 10, 8)
	kind, e3 := strconv.ParseUint(fields[2], 10, 8)
	digest := strings.ToUpper(strings.Join(fields[3:], ""))
	if e1 != nil || e2 != nil || e3 != nil || !regexp.MustCompile(`^[A-F0-9]+$`).MatchString(digest) {
		return ""
	}
	lengths := map[uint64]int{1: 40, 2: 64, 4: 96}
	if lengths[kind] != len(digest) {
		return ""
	}
	return fmt.Sprintf("%d %d %d %s", tag, algorithm, kind, digest)
}

func (m *ApplyManager) parentDS(ctx context.Context, domain string) ([]byte, error) {
	_, parent, ok := strings.Cut(domain, ".")
	if !ok {
		parent = "."
	}
	answer, err := m.dnsOutput(ctx, "/usr/bin/dig", "+time=3", "+tries=1", "+short", parent, "NS")
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Fields(string(answer)) {
		name = strings.TrimSuffix(name, ".")
		if !validation.IsValidHostname(name) {
			continue
		}
		result, err := m.dnsOutput(ctx, "/usr/bin/dig", "@"+name, "+time=3", "+tries=1", "+norecurse", "+noall", "+answer", "+comments", domain, "DS")
		if err == nil && strings.Contains(string(result), "status: NOERROR") && regexp.MustCompile(`flags: [^;]*\baa\b`).Match(result) {
			return result, nil
		}
	}
	return nil, fmt.Errorf("authoritative parent DS query unavailable")
}

func (m *ApplyManager) observeDNS(ctx context.Context, domain string, state dnsKeyState) (DNSAuthorityStatus, error) {
	status := DNSAuthorityStatus{Domain: domain, Phase: state.Phase, DS: []string{}, ParentDS: []string{}, RolloverNotBefore: state.PublishedAt.Add(2 * time.Duration(max(dnsKeyTTL, state.ParentDSTTL)) * time.Second)}
	ds, err := m.dnsDS(ctx, domain, state.Current)
	if err != nil {
		return status, err
	}
	status.DS = ds
	signed := m.target("/etc/nsd/zones/" + domain + ".signed")
	if _, err = m.dnsOutput(ctx, "/usr/bin/dnssec-verify", "-o", domain, signed); err == nil {
		status.LocalSignaturesValid = true
	}
	status.SignaturesExpireAt = signatureExpiry(signed)
	output, err := m.parentDS(ctx, domain)
	if err == nil && strings.Contains(string(output), "status: NOERROR") {
		status.ParentDSChecked = true
		for _, line := range strings.Split(string(output), "\n") {
			if value := dsValue(line); value != "" {
				status.ParentDS = append(status.ParentDS, value)
				fields := strings.Fields(line)
				if len(fields) > 1 {
					ttl, parseErr := strconv.Atoi(fields[1])
					if parseErr == nil && ttl > status.ParentDSTTL {
						status.ParentDSTTL = ttl
					}
				}
			}
		}
		for _, line := range ds {
			wanted := dsValue(line)
			for _, observed := range status.ParentDS {
				if wanted != "" && wanted == observed {
					status.ParentDSMatches = true
				}
			}
		}
	}
	if status.ParentDSChecked && len(status.ParentDS) == 0 {
		proof, proofErr := m.dnsOutput(ctx, "/usr/bin/delv", domain, "DS")
		status.ParentAbsenceValidated = proofErr == nil && strings.Contains(string(proof), "fully validated")
		status.CanDisable = status.ParentAbsenceValidated && !state.DSAbsentSince.IsZero() && time.Since(state.DSAbsentSince) >= 2*time.Duration(max(dnsKeyTTL, state.ParentDSTTL))*time.Second
	}
	if status.ParentDSMatches {
		validationOutput, validationErr := m.dnsOutput(ctx, "/usr/bin/delv", domain, "DNSKEY")
		status.ChainValid = validationErr == nil && strings.Contains(string(validationOutput), "fully validated")
	}
	status.RolloverNotBefore = state.PublishedAt.Add(2 * time.Duration(max(dnsKeyTTL, state.ParentDSTTL, status.ParentDSTTL)) * time.Second)
	status.Ready = status.LocalSignaturesValid && status.ParentDSChecked && status.ParentDSMatches && status.ChainValid && !status.SignaturesExpireAt.Before(time.Now().Add(time.Hour))
	return status, nil
}
func signatureExpiry(path string) time.Time {
	raw, err := os.ReadFile(path) //nolint:gosec // Callers provide a generated signed-zone path, never a client-selected path.
	if err != nil {
		return time.Time{}
	}
	var clean strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line, _, _ = strings.Cut(line, ";")
		clean.WriteString(line)
		clean.WriteByte(' ')
	}
	fields := strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(clean.String()))
	var expiry time.Time
	for i, field := range fields {
		if field == "RRSIG" && i+5 < len(fields) {
			parsed, err := time.Parse("20060102150405", fields[i+5])
			if err == nil && (expiry.IsZero() || parsed.Before(expiry)) {
				expiry = parsed
			}
		}
	}
	return expiry
}
func (m *ApplyManager) signDNSZone(ctx context.Context, domain string) error {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	state, err := m.loadDNSState(domain)
	if err != nil {
		return fmt.Errorf("DNSSEC keys must be prepared before enabling signing: %w", err)
	}
	if len(state.Current.Keys) != 2 {
		return fmt.Errorf("DNSSEC key generation incomplete")
	}
	directory := m.dnsDirectory(domain)
	keySet, err := os.MkdirTemp(m.dnsRoot(), ".signing-keys-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(keySet) }()
	generations := []dnsKeyGeneration{state.Current}
	if state.Previous != nil && state.Phase != "retired" {
		generations = append(generations, *state.Previous)
	}
	for _, generation := range generations {
		for _, key := range generation.Keys {
			if !regexp.MustCompile(`^K` + regexp.QuoteMeta(domain) + `\.\+013\+[0-9]{5}$`).MatchString(key) {
				return fmt.Errorf("invalid persisted key reference")
			}
			for _, suffix := range []string{".key", ".private"} {
				data, readErr := readDNSPrivateKey(filepath.Join(directory, key+suffix))
				if readErr != nil {
					return readErr
				}
				if writeErr := os.WriteFile(filepath.Join(keySet, key+suffix), data, 0600); writeErr != nil { //nolint:gosec // Canonical BIND key names and fixed suffixes in a private temporary directory.
					return writeErr
				}
			}
		}
	}
	input := m.target("/etc/nsd/zones/" + domain + ".txt")
	output := m.target("/etc/nsd/zones/" + domain + ".signed")
	temporary, err := os.CreateTemp(filepath.Dir(output), ".urmail-sign-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	_ = temporary.Close()
	defer func() { _ = os.Remove(name) }()
	_, err = m.dnsOutput(ctx, "/usr/bin/dnssec-signzone", "-n", "1", "-S", "-K", keySet, "-d", directory, "-o", domain, "-N", "keep", "-s", "now-300", "-e", "+2592000", "-f", name, input)
	if err != nil {
		return err
	}
	if _, err = m.dnsOutput(ctx, "/usr/bin/dnssec-verify", "-o", domain, name); err != nil {
		return err
	}
	if _, err = m.dnsOutput(ctx, "/usr/sbin/nsd-checkzone", domain, name); err != nil {
		return err
	}
	if err = os.Chmod(name, 0644); err != nil {
		return err
	}
	if err = os.Rename(name, output); err != nil {
		return err
	}
	if state.PublishedAt.IsZero() {
		state.PublishedAt = time.Now().UTC()
	}
	if state.Phase == "prepared" {
		state.Phase = "signed"
	}
	return m.saveDNSState(domain, state)
}

// DNSAuthorityStatus rechecks zone ownership before observing public DNS evidence.
func (m *ApplyManager) DNSAuthorityStatus(raw json.RawMessage) (string, string, error) {
	var params DNSAuthorityParams
	if err := strictParams(raw, &params); err != nil {
		return "", "", err
	}
	if !dnsDomain(params.Domain) || params.Action != "" {
		return "", "", fmt.Errorf("%w: invalid DNS status selector", ErrRejected)
	}
	if m.cfg != nil && m.cfg.DNSSecondaryOnly {
		return m.secondaryDNSStatus(params.Domain)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	metadata, authErr := m.authorizedDNS(ctx, params.Domain, params.TenantID, params.DomainID, params.ActorID)
	if authErr != nil {
		return "", "", authErr
	}
	state, err := m.loadDNSState(params.Domain)
	if os.IsNotExist(err) {
		data, marshalErr := json.Marshal(DNSAuthorityStatus{Domain: params.Domain, Phase: "unprepared", Enabled: metadata.DnssecEnabled.Bool, DS: []string{}, ParentDS: []string{}})
		return string(data), "", marshalErr
	}
	if err != nil {
		return "", "", err
	}
	status, err := m.observeDNS(ctx, params.Domain, state)
	if status.ParentDSChecked && len(status.ParentDS) > 0 {
		m.dnsMu.Lock()
		latest, loadErr := m.loadDNSState(params.Domain)
		if loadErr == nil {
			latest.ParentDSTTL = max(latest.ParentDSTTL, status.ParentDSTTL)
			latest.DSAbsentSince = time.Time{}
			loadErr = m.saveDNSState(params.Domain, latest)
		}
		m.dnsMu.Unlock()
		if loadErr != nil {
			return "", "", loadErr
		}
	}
	status.Enabled = metadata.DnssecEnabled.Bool
	if metadata.SecondaryHostname.String != "" {
		status.Secondary = &nsd.SecondaryConfig{Hostname: metadata.SecondaryHostname.String, Address: metadata.SecondaryAddress.String, PrimaryAddress: metadata.PrimaryAddress.String, TSIGKeyName: metadata.TsigKeyName.String}
		status.PrimarySOA = m.dnsSOA(ctx, status.Secondary.PrimaryAddress, params.Domain)
		status.SecondarySOA = m.dnsSOA(ctx, status.Secondary.Address, params.Domain)
		status.SecondaryAnswering = status.SecondarySOA != ""
		status.SecondarySOAMatches = status.PrimarySOA != "" && status.PrimarySOA == status.SecondarySOA
	}
	if err != nil {
		return "", "", err
	}
	data, err := json.Marshal(status)
	return string(data), "", err
}

// ManageDNSAuthority validates one named lifecycle step without arbitrary paths or commands.
func (m *ApplyManager) ManageDNSAuthority(raw json.RawMessage) (string, string, error) {
	var params DNSAuthorityParams
	if err := strictParams(raw, &params); err != nil {
		return "", "", err
	}
	if !dnsDomain(params.Domain) {
		return "", "", fmt.Errorf("%w: invalid DNS zone", ErrRejected)
	}
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	if m.cfg != nil && (m.cfg.WorkersHeld || m.cfg.Quarantined || m.cfg.DevMode) {
		return "", "", fmt.Errorf("%w: DNS mutations are held", ErrRejected)
	}
	if m.cfg != nil && m.cfg.DNSSecondaryOnly {
		if params.Action != "secondary-install" {
			return "", "", fmt.Errorf("%w: DNS-only mode only accepts protected secondary installation", ErrRejected)
		}
		return m.installSecondaryDNS(params.Domain)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	metadata, err := m.authorizedDNS(ctx, params.Domain, params.TenantID, params.DomainID, params.ActorID)
	if err != nil {
		return "", "", err
	}
	state, err := m.loadDNSState(params.Domain)
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	setIntent := func(enabled bool, secondary *nsd.SecondaryConfig) error {
		row := store.PlatformSetDNSSecurityParams{Fqdn: params.Domain, TenantID: metadata.OwnerTenantID, ID: metadata.RegisteredDomainID, DnssecEnabled: enabled, SecondaryHostname: metadata.SecondaryHostname.String, SecondaryAddress: metadata.SecondaryAddress.String, PrimaryAddress: metadata.PrimaryAddress.String, TsigKeyName: metadata.TsigKeyName.String}
		if secondary != nil {
			candidate := DNSSecondaryBundle{Version: 1, Domain: params.Domain, Hostname: secondary.Hostname, Address: secondary.Address, PrimaryAddress: secondary.PrimaryAddress, KeyName: secondary.TSIGKeyName, Secret: base64.StdEncoding.EncodeToString(make([]byte, 32))}
			if err := validateDNSSecondaryBundle(candidate); err != nil {
				return err
			}
			if len(secondary.Zones) != 0 || (secondary.TSIGAlgorithm != "" && secondary.TSIGAlgorithm != "hmac-sha256") {
				return fmt.Errorf("%w: invalid secondary scope or algorithm", ErrRejected)
			}
			row.SecondaryHostname = secondary.Hostname
			row.SecondaryAddress = secondary.Address
			row.PrimaryAddress = secondary.PrimaryAddress
			row.TsigKeyName = secondary.TSIGKeyName
		}
		_, writeErr := store.New(m.pool.Pool).PlatformSetDNSSecurity(ctx, row)
		return writeErr
	}
	switch params.Action {
	case "enable":
		if len(state.Current.Keys) != 2 {
			return "", "", fmt.Errorf("%w: prepare DNSSEC keys first", ErrRejected)
		}
		if err = setIntent(true, nil); err != nil {
			return "", "", err
		}
		return `{"changed":true,"reconcile_required":true}`, "", nil
	case "disable-check":
		observation, observeErr := m.observeDNS(ctx, params.Domain, state)
		if observeErr != nil || !observation.ParentAbsenceValidated {
			return "", "", fmt.Errorf("%w: cryptographically validated parent DS absence required", ErrRejected)
		}
		if state.DSAbsentSince.IsZero() {
			state.DSAbsentSince = time.Now().UTC()
		}
	case "disable":
		observation, observeErr := m.observeDNS(ctx, params.Domain, state)
		if observeErr != nil || !observation.CanDisable {
			return "", "", fmt.Errorf("%w: verified DS removal and cache wait required", ErrRejected)
		}
		if err = setIntent(false, nil); err != nil {
			return "", "", err
		}
		return `{"changed":true,"reconcile_required":true}`, "", nil
	case "secondary-configure":
		if params.Secondary == nil {
			return "", "", fmt.Errorf("%w: secondary parameters required", ErrRejected)
		}
		if err = setIntent(metadata.DnssecEnabled.Bool, params.Secondary); err != nil {
			return "", "", err
		}
		return `{"changed":true,"reconcile_required":true}`, "", nil

	case "prepare":
		if len(state.Current.Keys) != 0 {
			return "", "", fmt.Errorf("%w: keys already exist", ErrRejected)
		}
		state.Current, err = m.prepareDNSKeys(ctx, params.Domain)
		state.Phase = "prepared"
	case "rotate":
		if state.Phase == "retired" {
			state.Previous = nil
		}
		if state.Previous != nil || len(state.Current.Keys) != 2 || state.PublishedAt.IsZero() {
			return "", "", fmt.Errorf("%w: current signed generation required", ErrRejected)
		}
		observation, observeErr := m.observeDNS(ctx, params.Domain, state)
		if observeErr != nil || !observation.Ready {
			return "", "", fmt.Errorf("%w: validate current delegation before rollover", ErrRejected)
		}
		state.ParentDSTTL = observation.ParentDSTTL
		old := state.Current
		state.Current, err = m.prepareDNSKeys(ctx, params.Domain)
		state.Previous = &old
		state.Phase = "dual-signing"
		state.PublishedAt = time.Time{}
	case "retire":
		if state.Previous == nil {
			return "", "", fmt.Errorf("%w: no rollover in progress", ErrRejected)
		}
		observation, observeErr := m.observeDNS(ctx, params.Domain, state)
		previousDS, dsErr := m.dnsDSFromStoredGeneration(ctx, params.Domain, *state.Previous)
		oldStillPublished := false
		for _, old := range previousDS {
			for _, parent := range observation.ParentDS {
				if dsValue(old) == parent {
					oldStillPublished = true
				}
			}
		}
		if observeErr != nil || dsErr != nil || oldStillPublished || !observation.Ready || time.Now().Before(observation.RolloverNotBefore) {
			return "", "", fmt.Errorf("%w: successor DS validation and two DNSKEY TTL waits required", ErrRejected)
		}
		state.Phase = "retired"
	case "rollback":
		if state.Previous == nil {
			return "", "", fmt.Errorf("%w: no previous DNS key generation", ErrRejected)
		}
		if err = m.restoreDNSGeneration(params.Domain, *state.Previous); err == nil {
			successor := state.Current
			state.Current = *state.Previous
			state.Previous = &successor
			state.Phase = "dual-signing"
			state.PublishedAt = time.Now().UTC()
		}
	case "secondary-prepare":
		if metadata.SecondaryHostname.String == "" {
			return "", "", fmt.Errorf("%w: secondary intent required", ErrRejected)
		}
		if state.TSIGName != metadata.TsigKeyName.String {
			secret := make([]byte, 32)
			if _, err = rand.Read(secret); err == nil {
				state.TSIGName = metadata.TsigKeyName.String
				state.TSIGSecret = base64.StdEncoding.EncodeToString(secret)
			}
		}
	case "secondary-export":
		return m.exportSecondaryDNS(params.Domain, metadata, state)
	case "backup-manifest":
		return m.dnsBackupManifest(params.Domain, state)
	default:
		return "", "", fmt.Errorf("%w: unknown DNS action", ErrRejected)
	}
	if err != nil {
		return "", "", err
	}
	if err = m.saveDNSState(params.Domain, state); err != nil {
		return "", "", err
	}
	if params.Action == "retire" {
		if err = m.archiveDNSGeneration(params.Domain, *state.Previous); err != nil {
			return "", "", err
		}
	}
	return `{"changed":true}`, "", nil
}
func (m *ApplyManager) dnsArchiveDirectory(domain string, generation dnsKeyGeneration) string {
	return filepath.Join(m.dnsDirectory(domain), "retired", strconv.FormatInt(generation.CreatedAt.UnixNano(), 10))
}

func (m *ApplyManager) archiveDNSGeneration(domain string, generation dnsKeyGeneration) error {
	directory := m.dnsDirectory(domain)
	archive := m.dnsArchiveDirectory(domain, generation)
	if err := protectedDNSDirectory(archive); err != nil {
		return err
	}
	for _, key := range generation.Keys {
		for _, suffix := range []string{".key", ".private"} {
			old := filepath.Join(directory, key+suffix)
			target := filepath.Join(archive, key+suffix)
			if err := os.Rename(old, target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
func (m *ApplyManager) restoreDNSGeneration(domain string, generation dnsKeyGeneration) error {
	directory := m.dnsDirectory(domain)
	for _, key := range generation.Keys {
		for _, suffix := range []string{".key", ".private"} {
			target := filepath.Join(directory, key+suffix)
			if _, err := os.Stat(target); err == nil {
				continue
			}
			if err := os.Rename(filepath.Join(m.dnsArchiveDirectory(domain, generation), key+suffix), target); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *ApplyManager) dnsBackupManifest(domain string, state dnsKeyState) (string, string, error) {
	directory := m.dnsDirectory(domain)
	manifest := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in key backup")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return fmt.Errorf("invalid private key backup entry")
		}
		data, err := os.ReadFile(path) //nolint:gosec // Private zone-tree walk rejects symlinks, non-regular entries and oversized files before hashing.
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		manifest[name] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		return "", "", err
	}
	data, err := json.Marshal(map[string]any{"domain": domain, "phase": state.Phase, "files": manifest, "requires_encrypted_backup": true})
	return string(data), "", err
}

// DNSSecondaryBundle is transported only as a root-owned, private file.
type DNSSecondaryBundle struct {
	Version        int    `json:"version"`
	Domain         string `json:"domain"`
	Hostname       string `json:"hostname"`
	Address        string `json:"address"`
	PrimaryAddress string `json:"primary_address"`
	KeyName        string `json:"key_name"`
	Secret         string `json:"secret"`
}

func validateDNSSecondaryBundle(bundle DNSSecondaryBundle) error {
	secret, err := base64.StdEncoding.DecodeString(bundle.Secret)
	if bundle.Version != 1 || !dnsDomain(bundle.Domain) || !dnsDomain(bundle.Hostname) || net.ParseIP(bundle.Address) == nil || net.ParseIP(bundle.PrimaryAddress) == nil || !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`).MatchString(bundle.KeyName) || err != nil || len(secret) != 32 {
		return fmt.Errorf("%w: invalid secondary bundle", ErrRejected)
	}
	return nil
}
func (m *ApplyManager) exportSecondaryDNS(domain string, metadata store.PlatformGetDNSSecurityRow, state dnsKeyState) (string, string, error) {
	bundle := DNSSecondaryBundle{Version: 1, Domain: domain, Hostname: metadata.SecondaryHostname.String, Address: metadata.SecondaryAddress.String, PrimaryAddress: metadata.PrimaryAddress.String, KeyName: state.TSIGName, Secret: state.TSIGSecret}
	if err := validateDNSSecondaryBundle(bundle); err != nil {
		return "", "", err
	}
	directory := filepath.Join(m.dnsRoot(), "exports")
	if err := protectedDNSDirectory(directory); err != nil {
		return "", "", err
	}
	data, err := json.Marshal(bundle) //nolint:gosec // This root-only 0600 export file must carry TSIG to the authorized secondary; it never enters HTTP, logs or PostgreSQL.
	if err != nil {
		return "", "", err
	}
	path, err := writeFileAtomic(filepath.Join(directory, domain+".json"), data, 0600, []string{m.dnsRoot()})
	if err != nil {
		return "", "", err
	}
	result, err := json.Marshal(map[string]string{"protected_bundle": path})
	return string(result), "", err
}

func (m *ApplyManager) dnsSOA(ctx context.Context, address, domain string) string {
	if net.ParseIP(address) == nil || !dnsDomain(domain) {
		return ""
	}
	raw, err := m.dnsOutput(ctx, "/usr/bin/dig", "@"+address, "+time=2", "+tries=1", "+short", domain, "SOA")
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 7 {
		return ""
	}
	for _, value := range fields[2:] {
		if _, err := strconv.ParseUint(value, 10, 32); err != nil {
			return ""
		}
	}
	return strings.Join(fields, " ")
}

func readDNSPrivateKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	uid, _ := fileOwner(info)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || uid != os.Geteuid() || info.Size() > 65536 {
		return nil, fmt.Errorf("unsafe DNS key file")
	}
	return os.ReadFile(path) //nolint:gosec // Canonical BIND basename and fixed suffix are checked by the caller; regular root-owned private file validated above.
}

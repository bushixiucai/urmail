package miadbinventory

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed private_source.py
var privateSourceReader string

const privateLoaderVersion = "miab-reviewed-private-core-v1"

// ReviewedSourceOptions requires a concrete metadata review and explicit files.
type ReviewedSourceOptions struct {
	Options
	ExpectedSnapshotSHA256 string
}

// ReviewedDomain is a private target-domain proposal, never a control proof.
type ReviewedDomain struct {
	FQDN      string `json:"-"`
	TenantRef string `json:"-"`
	SourceRef string `json:"-"`
}

// ReviewedMailbox carries exact source credentials only to the privileged runner.
type ReviewedMailbox struct {
	SourceID         int64           `json:"-"`
	Address          string          `json:"-"`
	FQDN             string          `json:"-"`
	LocalPart        string          `json:"-"`
	PasswordHash     string          `json:"-"`
	QuotaBytes       int64           `json:"-"`
	LegacyPrivileges string          `json:"-"`
	ExtraJSON        json.RawMessage `json:"-"`
}

// ReviewedAlias separates receive routing from manual DIRECT sender authority.
type ReviewedAlias struct {
	SourceRefs   []string `json:"-"`
	FQDN         string   `json:"-"`
	LocalPart    string   `json:"-"`
	Target       string   `json:"-"`
	SenderLogins []string `json:"-"`
}

// ReviewedSource has no public constructor or mutable record storage. It is only
// a core metadata proposal; source content/activation proofs remain outstanding.
type ReviewedSource struct {
	options                   ReviewedSourceOptions
	fingerprint, privateProof string
	summary                   Report
	domains                   []ReviewedDomain
	mailboxes                 []ReviewedMailbox
	aliases                   []ReviewedAlias
}

func (ReviewedDomain) String() string { return "ReviewedDomain{redacted}" }

// GoString redacts private domain names from developer formatting.
func (d ReviewedDomain) GoString() string { return d.String() }

// MarshalJSON never serializes a private domain proposal.
func (ReviewedDomain) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }
func (ReviewedMailbox) String() string              { return "ReviewedMailbox{redacted}" }

// GoString keeps mailbox credentials out of developer formatting.
func (m ReviewedMailbox) GoString() string { return m.String() }

// MarshalJSON emits only a redaction marker, never credentials or provenance.
func (ReviewedMailbox) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }
func (ReviewedAlias) String() string                 { return "ReviewedAlias{redacted}" }

// GoString keeps private receive and sender addresses out of debug output.
func (a ReviewedAlias) GoString() string { return a.String() }

// MarshalJSON never emits private alias addresses or sender grants.
func (ReviewedAlias) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }
func (*ReviewedSource) String() string             { return "ReviewedSource{private-core-only}" }

// GoString hides all private source fields.
func (s *ReviewedSource) GoString() string { return s.String() }

// MarshalJSON exposes pending scope only, never any source records.
func (s *ReviewedSource) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct{ CoreOnly, SievePending bool }{true, true})
}

// Fingerprint is the exact metadata digest approved by mapping version 2.
func (s *ReviewedSource) Fingerprint() string {
	if s == nil {
		return ""
	}
	return s.fingerprint
}

// PrivateProofSHA256 binds full SQLite/config/mapping bytes and loader/schema versions.
func (s *ReviewedSource) PrivateProofSHA256() string {
	if s == nil {
		return ""
	}
	return s.privateProof
}

// CoreOnly prevents metadata import from being represented as completed migration.
func (*ReviewedSource) CoreOnly() bool { return true }

// SievePending keeps original/unknown source rules pending separate activation proof.
func (*ReviewedSource) SievePending() bool { return true }

// PendingProofs returns independent outstanding clone and service acceptance gates.
func (*ReviewedSource) PendingProofs() []string {
	return []string{"maildir_dkim_private_clone_binding", "raw_sieve_and_managesieve_activation", "identity_session_and_device_state", "generation_protocol_and_client_acceptance"}
}

// Domains returns a detached copy of private proposals.
func (s *ReviewedSource) Domains() []ReviewedDomain {
	if s == nil {
		return nil
	}
	return append([]ReviewedDomain(nil), s.domains...)
}

// Mailboxes returns a deep copy, including immutable provenance bytes.
func (s *ReviewedSource) Mailboxes() []ReviewedMailbox {
	if s == nil {
		return nil
	}
	out := append([]ReviewedMailbox(nil), s.mailboxes...)
	for i := range out {
		out[i].ExtraJSON = append(json.RawMessage(nil), out[i].ExtraJSON...)
	}
	return out
}

// Aliases returns detached source refs/sender lists; callers cannot rewrite review facts.
func (s *ReviewedSource) Aliases() []ReviewedAlias {
	if s == nil {
		return nil
	}
	out := append([]ReviewedAlias(nil), s.aliases...)
	for i := range out {
		out[i].SourceRefs = append([]string(nil), out[i].SourceRefs...)
		out[i].SenderLogins = append([]string(nil), out[i].SenderLogins...)
	}
	return out
}

// Summary returns only the already redacted public report and detached slices.
func (s *ReviewedSource) Summary() Report {
	if s == nil {
		return Report{}
	}
	raw, _ := json.Marshal(s.summary)
	var result Report
	_ = json.Unmarshal(raw, &result)
	return result
}

// ValidateUnchanged must be checked again before an atomic target commit.
func (s *ReviewedSource) ValidateUnchanged(ctx context.Context) error {
	if s == nil || s.fingerprint == "" || s.privateProof == "" {
		return errors.New("reviewed private source is not initialized")
	}
	proof, err := privateInputProof(ctx, s.options)
	if err != nil || proof.digest != s.privateProof {
		return errors.New("reviewed source bytes or loader context changed")
	}
	return nil
}

type privateInputState struct {
	digest       string
	mappingBytes []byte
}

func privateInputProof(ctx context.Context, options ReviewedSourceOptions) (privateInputState, error) {
	var result privateInputState
	config, err := readRegular(options.ConfigPath, 64<<10)
	if err != nil {
		return result, errors.New("reviewed configuration is unavailable")
	}
	selected, err := readRegular(options.MappingPath, 1<<20)
	if err != nil {
		return result, errors.New("reviewed mapping is unavailable")
	}
	info, err := os.Lstat(options.SQLitePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return result, errors.New("private SQLite source is not a bounded regular file")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(options.SQLitePath + suffix); !os.IsNotExist(e) {
			return result, errors.New("private source must be a standalone immutable snapshot")
		}
	}
	file, err := os.Open(options.SQLitePath) // #nosec G304 G703 -- explicit read-only source, checked for stable identity and bounded whole-file proof.
	if err != nil {
		return result, errors.New("private SQLite source cannot be opened")
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, errors.New("private source identity changed")
	}
	h := sha256.New()
	buffer := make([]byte, 65536)
	var bytesRead int64
	for {
		if ctx.Err() != nil {
			return result, errors.New("private source proof canceled")
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			bytesRead += int64(n)
			if bytesRead > info.Size() || bytesRead > 512<<20 {
				return result, errors.New("private source grew during proof")
			}
			_, _ = h.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return result, errors.New("private source proof cannot be read")
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return result, errors.New("private source changed while hashing")
	}
	if bytesRead != info.Size() {
		return result, errors.New("private source shrank during proof")
	}
	configHash, mappingHash := sha256.Sum256(config), sha256.Sum256(selected)
	loaderCode := sha256.Sum256([]byte(privateSourceReader))
	binding := struct{ SQLite, Config, Mapping, Schema, Loader, MetadataReview string }{hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(configHash[:]), hex.EncodeToString(mappingHash[:]), "miab_v80_users_v1", privateLoaderVersion + ":" + hex.EncodeToString(loaderCode[:]), options.ExpectedSnapshotSHA256}
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	result.digest = hex.EncodeToString(digest[:])
	result.mappingBytes = selected
	return result, nil
}

type privateSourceUser struct {
	ID           int64           `json:"id"`
	Email        string          `json:"email"`
	PasswordHash string          `json:"password_hash"`
	Extra        json.RawMessage `json:"extra"`
	Privileges   string          `json:"privileges"`
	Quota        string          `json:"quota"`
}
type privateSourceWire struct {
	Fingerprint string              `json:"fingerprint"`
	Users       []privateSourceUser `json:"users"`
	Aliases     []sourceAlias       `json:"aliases"`
}

// LoadReviewedSource returns private data only after byte-immutable, exact v2
// review binding. The normal readonly report and its fixed reader are unchanged.
func LoadReviewedSource(ctx context.Context, options ReviewedSourceOptions) (*ReviewedSource, error) {
	if !reviewDigestPattern.MatchString(options.ExpectedSnapshotSHA256) {
		return nil, errors.New("an exact reviewed metadata fingerprint is required")
	}
	before, err := privateInputProof(ctx, options)
	if err != nil {
		return nil, err
	}
	report, err := Inspect(ctx, options.Options)
	if err != nil || report.Status != "dry_run_complete" || report.MappingVersion != 2 || !report.ReviewSnapshotBound || report.TargetTenantGroups != 1 || report.SnapshotSHA256 != options.ExpectedSnapshotSHA256 {
		return nil, errors.New("private source requires a successful exact version-2 single-tenant review")
	}
	absolute, err := filepath.Abs(options.SQLitePath)
	if err != nil {
		return nil, errors.New("private source path is invalid")
	}
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(timeout, "/usr/bin/python3", "-I", "-S", "-B", "-c", privateSourceReader, absolute, options.ExpectedSnapshotSHA256) // #nosec G204 G702 -- fixed isolated reader; credentials only flow through a bounded private pipe, never argv.
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}
	defer func() {
		raw := output.Bytes()
		for i := range raw {
			raw[i] = 0
		}
	}()
	if command.Run() != nil {
		return nil, errors.New("private reviewed source read failed")
	}
	var wire privateSourceWire
	if json.Unmarshal(output.Bytes(), &wire) != nil || wire.Fingerprint != options.ExpectedSnapshotSHA256 {
		return nil, errors.New("private source fingerprint or field types are inconsistent")
	}
	after, err := privateInputProof(ctx, options)
	if err != nil || after.digest != before.digest {
		return nil, errors.New("private source/config/mapping bytes changed during load")
	}
	var selected mapping
	if json.Unmarshal(before.mappingBytes, &selected) != nil || selected.Version != 2 || selected.ReviewedSnapshotSHA256 != wire.Fingerprint {
		return nil, errors.New("private mapping is inconsistent with its byte proof")
	}
	loaded := &ReviewedSource{options: options, fingerprint: wire.Fingerprint, privateProof: after.digest, summary: report}
	if err = loaded.buildReviewedRecords(wire, selected); err != nil {
		return nil, err
	}
	if int64(len(loaded.mailboxes)) != report.Counts["users"] || int64(len(wire.Aliases)) != report.Counts["aliases"]+report.Counts["auto_aliases"] {
		return nil, errors.New("private source counts differ from the reviewed snapshot")
	}
	return loaded, nil
}

func (s *ReviewedSource) buildReviewedRecords(wire privateSourceWire, selected mapping) error {
	tenants := map[string]string{}
	for _, d := range selected.Domains {
		domain, ok := canonicalDomain(d.Domain)
		if !ok {
			return errors.New("reviewed private domain is invalid")
		}
		tenants[domain] = d.TenantRef
	}
	userAddresses := map[string]bool{}
	for _, user := range wire.Users {
		address, domain, ok := canonicalAddress(user.Email, false)
		quota, err := strconv.ParseInt(user.Quota, 10, 64)
		if !ok || err != nil || quota < 0 || tenants[domain] == "" || user.PasswordHash == "" {
			return errors.New("private mailbox facts are inconsistent")
		}
		extra := string(user.Extra)
		if extra != "null" && extra != `""` {
			return errors.New("private extra metadata requires a separate preservation review")
		}
		if quota == 0 {
			quota = -1
		}
		local, _, _ := strings.Cut(address, "@")
		s.mailboxes = append(s.mailboxes, ReviewedMailbox{user.ID, address, domain, local, user.PasswordHash, quota, user.Privileges, append(json.RawMessage(nil), user.Extra...)})
		userAddresses[address] = true
	}
	keys := make([]string, 0, len(tenants))
	for domain := range tenants {
		keys = append(keys, domain)
	}
	sort.Strings(keys)
	for i, domain := range keys {
		s.domains = append(s.domains, ReviewedDomain{domain, tenants[domain], fmt.Sprintf("domain:%d", i+1)})
	}
	routes := map[string][]sourceAlias{}
	for _, alias := range wire.Aliases {
		address, _, ok := canonicalAddress(alias.Source, false)
		if !ok || alias.PermittedSenders != nil || strings.Contains(alias.Destination, ",") {
			return errors.New("private alias scope requires separate typed import support")
		}
		routes[address] = append(routes[address], alias)
	}
	colors := map[string]int{}
	cache := map[string]string{}
	var resolve func(string, int) (string, error)
	resolve = func(address string, depth int) (string, error) {
		if userAddresses[address] {
			return address, nil
		}
		if target, ok := cache[address]; ok {
			return target, nil
		}
		if colors[address] == 1 || depth > 1024 || len(routes[address]) == 0 {
			return "", errors.New("private receive graph is unproved")
		}
		colors[address] = 1
		final := ""
		for _, row := range routes[address] {
			target, _, ok := canonicalAddress(strings.TrimSpace(row.Destination), false)
			if !ok {
				return "", errors.New("private alias target is invalid")
			}
			leaf, err := resolve(target, depth+1)
			if err != nil {
				return "", err
			}
			if final != "" && leaf != final {
				return "", errors.New("private alias branches have different final mailboxes")
			}
			final = leaf
		}
		colors[address] = 2
		cache[address] = final
		return final, nil
	}
	keys = keys[:0]
	for address := range routes {
		keys = append(keys, address)
	}
	sort.Strings(keys)
	for _, address := range keys {
		final, err := resolve(address, 0)
		if err != nil {
			return err
		}
		local, domain, _ := strings.Cut(address, "@")
		record := ReviewedAlias{FQDN: domain, LocalPart: local, Target: final, SourceRefs: []string{}, SenderLogins: []string{}}
		senders := map[string]bool{}
		for _, row := range routes[address] {
			record.SourceRefs = append(record.SourceRefs, rowRef(row.Table, row.ID))
			if row.Table == "aliases" {
				direct, _, ok := canonicalAddress(strings.TrimSpace(row.Destination), false)
				if !ok {
					return errors.New("manual direct sender-login fact is invalid")
				}
				if userAddresses[direct] {
					senders[direct] = true
				}
			}
		}
		for sender := range senders {
			record.SenderLogins = append(record.SenderLogins, sender)
		}
		sort.Strings(record.SenderLogins)
		s.aliases = append(s.aliases, record)
	}
	return nil
}

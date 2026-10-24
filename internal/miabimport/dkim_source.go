package miabimport

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/bushixiucai/urmail/internal/miadbinventory"
)

// ErrDKIMSource rejects missing, changed or unsupported private source proofs.
var ErrDKIMSource = errors.New("LEGACY_DKIM_REVIEWED_SOURCE_REQUIRED")

const (
	legacyDKIMKey          = "home/user-data/mail/dkim/mail.private"
	legacyDKIMKeyTable     = "etc/opendkim/KeyTable"
	legacyDKIMSigningTable = "etc/opendkim/SigningTable"
)

var legacySelectorPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// DKIMSourceOptions binds fixed files in an explicitly selected private clone.
// No live source path, optional table or guessed selector is accepted.
type DKIMSourceOptions struct {
	CloneRoot                  string
	ExpectedPrivateSHA256      string
	ExpectedKeyTableSHA256     string
	ExpectedSigningTableSHA256 string
	ExpectedSnapshotSHA256     string
}

type reviewedDKIMDomain struct{ fqdn, selector string }

// ReviewedDKIM contains private original bytes without an exported secret getter.
// Only the privileged importer in this package can persist them to PostgreSQL.
type ReviewedDKIM struct {
	options                         DKIMSourceOptions
	source                          *miadbinventory.ReviewedSource
	privatePEM                      string
	domains                         []reviewedDKIMDomain
	publicFingerprint, privateProof string
}

func (ReviewedDKIM) String() string { return "ReviewedDKIM{redacted}" }

// GoString redacts private key material from developer formatting.
func (d ReviewedDKIM) GoString() string { return d.String() }

// MarshalJSON emits no key bytes or private domain routing.
func (ReviewedDKIM) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }

// PublicFingerprint is the SHA256 of the RSA SubjectPublicKeyInfo DER.
func (d *ReviewedDKIM) PublicFingerprint() string {
	if d == nil {
		return ""
	}
	return d.publicFingerprint
}

// PrivateProofSHA256 binds exact private/table bytes and the reviewed core source.
func (d *ReviewedDKIM) PrivateProofSHA256() string {
	if d == nil {
		return ""
	}
	return d.privateProof
}

// DomainCount reports the complete reviewed routing set without exposing names.
func (d *ReviewedDKIM) DomainCount() int {
	if d == nil {
		return 0
	}
	return len(d.domains)
}

// LoadReviewedDKIM reads only the fixed standalone clone files. It never rotates,
// normalizes, prints, publishes or passes the original key to an external process.
func LoadReviewedDKIM(ctx context.Context, options DKIMSourceOptions, source *miadbinventory.ReviewedSource) (*ReviewedDKIM, error) {
	if source == nil || source.Fingerprint() != options.ExpectedSnapshotSHA256 || !proofPattern.MatchString(options.ExpectedSnapshotSHA256) || source.ValidateUnchanged(ctx) != nil {
		return nil, ErrDKIMSource
	}
	for _, expected := range []string{options.ExpectedPrivateSHA256, options.ExpectedKeyTableSHA256, options.ExpectedSigningTableSHA256} {
		if !proofPattern.MatchString(expected) {
			return nil, ErrDKIMSource
		}
	}
	raw, tables, err := readLegacyDKIMInputs(ctx, options)
	if err != nil {
		return nil, ErrDKIMSource
	}
	key, err := parseLegacyRSA(raw)
	if err != nil {
		return nil, ErrDKIMSource
	}
	domains, err := parseLegacyDKIMTables(tables[0], tables[1], source)
	if err != nil {
		return nil, ErrDKIMSource
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, ErrDKIMSource
	}
	fingerprint := sha256.Sum256(publicDER)
	proof := legacyDKIMProof(options, source.PrivateProofSHA256())
	result := &ReviewedDKIM{options: options, source: source, privatePEM: string(raw), domains: domains, publicFingerprint: hex.EncodeToString(fingerprint[:]), privateProof: proof}
	if result.ValidateUnchanged(ctx) != nil {
		return nil, ErrDKIMSource
	}
	return result, nil
}

// ValidateUnchanged is required again immediately before a target commit.
func (d *ReviewedDKIM) ValidateUnchanged(ctx context.Context) error {
	if d == nil || d.source == nil || d.source.Fingerprint() != d.options.ExpectedSnapshotSHA256 || d.source.ValidateUnchanged(ctx) != nil {
		return ErrDKIMSource
	}
	if _, _, err := readLegacyDKIMInputs(ctx, d.options); err != nil {
		return ErrDKIMSource
	}
	if d.privateProof != legacyDKIMProof(d.options, d.source.PrivateProofSHA256()) {
		return ErrDKIMSource
	}
	return nil
}

func legacyDKIMProof(options DKIMSourceOptions, coreProof string) string {
	encoded, _ := json.Marshal([]string{"miab-v80-private-dkim-v1", options.ExpectedSnapshotSHA256, coreProof, options.ExpectedPrivateSHA256, options.ExpectedKeyTableSHA256, options.ExpectedSigningTableSHA256})
	proof := sha256.Sum256(encoded)
	return hex.EncodeToString(proof[:])
}

func parseLegacyRSA(raw []byte) (*rsa.PrivateKey, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN ")) {
		return nil, ErrDKIMSource
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrDKIMSource
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrDKIMSource
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrDKIMSource
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, ErrDKIMSource
		}
	default:
		return nil, ErrDKIMSource
	}
	if key.N.BitLen() != 2048 || key.Validate() != nil {
		return nil, ErrDKIMSource
	}
	return key, nil
}

func parseLegacyDKIMTables(keyTable, signing []byte, source *miadbinventory.ReviewedSource) ([]reviewedDKIMDomain, error) {
	expected := map[string]bool{}
	for _, domain := range source.Domains() {
		expected[domain.FQDN] = true
	}
	if len(expected) == 0 || len(expected) > 1000 {
		return nil, ErrDKIMSource
	}
	selectors := map[string]string{}
	for _, line := range strings.Split(string(keyTable), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		columns := strings.Fields(line)
		if len(columns) != 2 {
			return nil, ErrDKIMSource
		}
		fields := strings.Split(columns[1], ":")
		if len(fields) != 3 || columns[0] != fields[0] || !expected[fields[0]] || !legacySelectorPattern.MatchString(fields[1]) || fields[2] != "/"+legacyDKIMKey || selectors[fields[0]] != "" {
			return nil, ErrDKIMSource
		}
		selectors[fields[0]] = fields[1]
	}
	routed := map[string]bool{}
	for _, line := range strings.Split(string(signing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		columns := strings.Fields(line)
		if len(columns) != 2 || columns[0] != "*@"+columns[1] || !expected[columns[1]] || routed[columns[1]] {
			return nil, ErrDKIMSource
		}
		routed[columns[1]] = true
	}
	if len(selectors) != len(expected) || len(routed) != len(expected) {
		return nil, ErrDKIMSource
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]reviewedDKIMDomain, 0, len(names))
	for _, name := range names {
		if selectors[name] == "" || !routed[name] {
			return nil, ErrDKIMSource
		}
		result = append(result, reviewedDKIMDomain{fqdn: name, selector: selectors[name]})
	}
	return result, nil
}

func readLegacyDKIMInputs(ctx context.Context, options DKIMSourceOptions) ([]byte, [][]byte, error) {
	if !filepath.IsAbs(options.CloneRoot) || filepath.Clean(options.CloneRoot) != options.CloneRoot || options.CloneRoot == "/" {
		return nil, nil, ErrDKIMSource
	}
	root, err := unix.Open(options.CloneRoot, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, ErrDKIMSource
	}
	defer func() { _ = unix.Close(root) }()
	var st unix.Stat_t
	if unix.Fstat(root, &st) != nil || st.Mode&0077 != 0 || st.Uid != 0 && strconv.FormatUint(uint64(st.Uid), 10) != strconv.Itoa(os.Geteuid()) {
		return nil, nil, ErrDKIMSource
	}
	paths := []string{legacyDKIMKey, legacyDKIMKeyTable, legacyDKIMSigningTable}
	expected := []string{options.ExpectedPrivateSHA256, options.ExpectedKeyTableSHA256, options.ExpectedSigningTableSHA256}
	contents := make([][]byte, 3)
	for i, name := range paths {
		limit := 65536
		if i == 0 {
			limit = 32768
		}
		data, readErr := readLegacyNoFollow(ctx, root, name, limit)
		if readErr != nil {
			return nil, nil, ErrDKIMSource
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != expected[i] {
			return nil, nil, ErrDKIMSource
		}
		contents[i] = data
	}
	return contents[0], contents[1:], nil
}

func readLegacyNoFollow(ctx context.Context, root int, name string, limit int) ([]byte, error) {
	current, err := unix.Dup(root)
	if err != nil {
		return nil, ErrDKIMSource
	}
	defer func() { _ = unix.Close(current) }()
	components := strings.Split(name, "/")
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, ErrDKIMSource
		}
		_ = unix.Close(current)
		current = next
		var st unix.Stat_t
		if unix.Fstat(current, &st) != nil || st.Mode&0022 != 0 {
			return nil, ErrDKIMSource
		}
	}
	fd, err := unix.Openat(current, components[len(components)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrDKIMSource
	}
	defer func() { _ = unix.Close(fd) }()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0022 != 0 || before.Nlink != 1 || before.Size < 0 || before.Size > int64(limit) {
		return nil, ErrDKIMSource
	}
	data := make([]byte, 0, limit)
	buffer := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil, ErrDKIMSource
		}
		n, readErr := unix.Read(fd, buffer)
		if readErr == unix.EINTR {
			continue
		}
		if readErr != nil {
			return nil, ErrDKIMSource
		}
		if n == 0 {
			break
		}
		data = append(data, buffer[:n]...)
		if len(data) > limit {
			return nil, ErrDKIMSource
		}
	}
	if unix.Fstat(fd, &after) != nil {
		return nil, ErrDKIMSource
	}
	// Reading may update atime; identity, ownership, size and modification/change
	// times must remain stable. Full expected digests bind all content bytes.
	before.Atim = after.Atim
	if before != after || int64(len(data)) != before.Size {
		return nil, ErrDKIMSource
	}
	return data, nil
}

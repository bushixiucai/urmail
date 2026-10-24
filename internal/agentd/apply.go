package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/opendkim"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

// OpConfigApply applies a validated domain snapshot.
const OpConfigApply = "config.apply"

// OpConfigStatus reads the applied generation state.
const OpConfigStatus = "config.status"

// StateRoot is private to the privileged agent.
const StateRoot = "/var/lib/urmail-agent"

// ApplyParams deliberately excludes paths, node configuration and file bodies.
// The caller can only supply bounded domain data consumed by pure generators.
type ApplyParams struct {
	Postfix postfix.Snapshot  `json:"postfix"`
	DNS     nsd.Snapshot      `json:"dns"`
	DKIM    opendkim.Snapshot `json:"dkim"`
}

// ApplyStatus reports desired and applied generation health.
type ApplyStatus struct {
	Desired          string    `json:"desired_generation"`
	Applied          string    `json:"applied_generation"`
	AppliedAt        time.Time `json:"applied_at"`
	Ready            bool      `json:"ready"`
	Degraded         bool      `json:"degraded"`
	Step             string    `json:"step"`
	Error            string    `json:"error,omitempty"`
	CertificateError string    `json:"certificate_error,omitempty"`
}

type savedFile struct {
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode"`
	Exists  bool   `json:"exists"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}
type applyJournal struct {
	Files           map[string]savedFile `json:"files"`
	Previous        ApplyStatus          `json:"previous"`
	PreviousPaths   []string             `json:"previous_paths"`
	PreviousDomains []string             `json:"previous_domains"`
	Reloaded        []string             `json:"reloaded"`
}
type applyManifest struct {
	Status  ApplyStatus `json:"status"`
	Paths   []string    `json:"paths"`
	Domains []string    `json:"domains"`
}

// CommandRunner executes a fixed privileged command with a deadline.
type CommandRunner func(context.Context, string, ...string) error

// ApplyManager is the only production writer of mail service configuration.
// All transactions are serialized, and its state directory is root-owned.
type ApplyManager struct {
	mu                    sync.Mutex
	certMu                sync.Mutex
	pool                  *db.Pool
	versions              reconcile.ZoneVersionStore
	published             atomic.Value
	cfg                   *config.Config
	root                  string
	run                   CommandRunner
	readinessProbe        func(context.Context) error
	output                func(context.Context, ...string) ([]byte, error)
	status                ApplyStatus
	paths                 []string
	domains               []string
	targetRoot            string
	webroot               string
	automaticCertificates bool
	certificatePending    atomic.Bool
	fileWriter            func(string, []byte, os.FileMode, []string) (string, error)
}

// NewApplyManager creates the serialized root configuration controller.
func NewApplyManager(cfg *config.Config) (*ApplyManager, error) {
	m := &ApplyManager{cfg: cfg, root: StateRoot, run: runCommand, automaticCertificates: true, fileWriter: writeFileAtomic}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return nil, err
	}
	m.pool = pool
	m.versions = &reconcile.VersionedStore{StoreQuerier: store.New(pool.Pool), DB: pool.Pool}

	if err := os.MkdirAll(m.root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(m.root, 0700); err != nil {
		return nil, err
	}
	var manifest applyManifest
	if raw, err := os.ReadFile(filepath.Join(m.root, "manifest.json")); err == nil {
		if err = json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("read generation manifest: %w", err)
		}
		m.status = manifest.Status
		m.status.Ready = false
		m.status.Step = "startup"
		m.paths = manifest.Paths
		m.domains = manifest.Domains
	}
	if err := m.recover(); err != nil {
		return nil, err
	}
	m.publish()
	return m, nil
}

func strictParams(raw json.RawMessage, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("%w: invalid structured parameters", ErrRejected)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: trailing parameters", ErrRejected)
	}
	return nil
}

// Status returns a public, secret-free generation snapshot.
func (m *ApplyManager) Status(raw json.RawMessage) (string, string, error) {
	var empty struct{}
	if err := strictParams(raw, &empty); err != nil {
		return "", "", err
	}
	value := m.published.Load()
	status := ApplyStatus{Step: "startup"}
	if value != nil {
		status = value.(ApplyStatus)
	}
	b, err := json.Marshal(status)
	return string(b), "", err
}

// Apply validates, checks, installs and probes one canonical generation.
func (m *ApplyManager) Apply(raw json.RawMessage) (stdout, stderr string, resultErr error) {
	var p ApplyParams
	if err := strictParams(raw, &p); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() {
		if resultErr != nil {
			m.status.Ready = false
			if m.status.Error == "" {
				m.status.Error = "configuration apply failed at " + m.status.Step
			}
		}
		m.publish()
	}()
	if err := m.ensureBootstrap(p); err != nil {
		m.status.Ready = false
		m.status.Step = "certificate-bootstrap"
		m.status.Error = "certificate bootstrap failed"
		m.publish()
		return "", "", err
	}
	files, err := m.render(p)
	if err != nil {
		m.status.Ready = false
		m.status.Step = "render"
		m.status.Error = "invalid domain snapshot"
		m.publish()
		return "", "", fmt.Errorf("%w: invalid domain snapshot: %w", ErrRejected, err)
	}
	files = filterPreservedExternalFiles(m.cfg, files)
	sum := sha256.New()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		_, _ = sum.Write([]byte(path + "\x00" + files[path] + "\x00"))
	}
	generation := hex.EncodeToString(sum.Sum(nil))
	if m.status.Ready && m.status.Applied == generation {
		m.status.Ready = false
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.checkMailReadiness(ctx); err != nil {
			m.status.Error = "configuration readiness failed at " + m.status.Step + ": " + err.Error()
			m.publish()
			return "", "", fmt.Errorf("%w: %s", ErrFailed, m.status.Error)
		}
		m.status.Step = "ready"
		m.status.Ready = true
		m.publish()
		return generation, "", nil
	}
	previous := m.status
	m.status.Desired = generation
	m.status.Ready = false
	m.status.Error = ""
	m.status.Step = "stage"
	m.publish()
	stage := filepath.Join(m.root, "staging")
	if err := os.RemoveAll(stage); err != nil {
		return "", "", err
	}
	m.status.Step = "stage:export"
	if err := mail.ExportFiles(stage, files); err != nil {
		return "", "", err
	}
	journal := applyJournal{Files: map[string]savedFile{}, Previous: previous, PreviousPaths: append([]string{}, m.paths...), PreviousDomains: append([]string{}, m.domains...)}
	targets := append(append([]string{}, m.paths...), paths...)
	for _, path := range append(append([]string{}, m.paths...), paths...) {
		if strings.HasSuffix(path, ".map") {
			targets = append(targets, path+".db")
		}
	}
	for _, path := range targets {
		if _, ok := journal.Files[path]; ok {
			continue
		}
		m.status.Step = "stage:" + path
		if _, _, err := resolveAllowedPath(path, allowedWriteRoots); err != nil {
			return "", "", err
		}
		saved, err := saveFile(m.target(path))
		if err != nil {
			return "", "", err
		}
		journal.Files[path] = saved
	}
	m.status.Step = "stage:journal"
	if err := m.saveJSON("pending.json", journal); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	applyErr := func() error {
		m.status.Step = "write"
		m.publish()
		for _, path := range paths {
			m.status.Step = "write:" + path
			m.publish()
			if err := m.write(path, []byte(files[path]), runtimeMode(path)); err != nil {
				return err
			}
		}
		for _, path := range m.paths {
			if _, ok := files[path]; !ok {
				if preservedExternalPath(m.cfg, path) {
					continue
				}
				if err := os.Remove(m.target(path)); err != nil && !os.IsNotExist(err) {
					return err
				}
				if strings.HasSuffix(path, ".map") {
					if err := os.Remove(m.target(path + ".db")); err != nil && !os.IsNotExist(err) {
						return err
					}
				}
			}
		}
		m.status.Step = "check"
		m.publish()
		if err := m.check(ctx, p); err != nil {
			return err
		}
		m.status.Step = "postmap"
		m.publish()
		if len(paths) > 0 {
			journal.Reloaded = append(journal.Reloaded, "postfix")
			if err := m.saveJSON("pending.json", journal); err != nil {
				return err
			}
		}
		for _, path := range paths {
			if strings.HasSuffix(path, ".map") {
				if err := m.run(ctx, "/usr/sbin/postmap", path); err != nil {
					return err
				}
				if err := os.Chmod(m.target(path+".db"), 0644); err != nil {
					return err
				}
			}
		}
		m.status.Step = "reload"
		m.publish()
		for _, unit := range configUnits(m.cfg) {
			m.status.Step = "reload:" + unit
			m.publish()
			if !containsUnit(journal.Reloaded, unit) {
				journal.Reloaded = append(journal.Reloaded, unit)
			}
			if err := m.saveJSON("pending.json", journal); err != nil {
				return err
			}
			if err := m.run(ctx, "/usr/bin/systemctl", "restart", unit); err != nil {
				return fmt.Errorf("restart %s: %w", unit, err)
			}
		}
		m.status.Step = "probe"
		m.publish()
		for _, unit := range ConfigUnits {
			m.status.Step = "probe:" + unit
			m.publish()
			if err := m.run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit); err != nil {
				return fmt.Errorf("probe %s: %w", unit, err)
			}
		}
		if err := m.checkMailReadiness(ctx); err != nil {
			return err
		}
		return nil
	}()
	if applyErr != nil {
		failedStep := m.status.Step
		rollbackErr := m.restore(journal)
		m.status = previous
		m.status.Desired = generation
		m.status.Ready = false
		m.status.Step = failedStep
		m.status.Error = "configuration apply failed at " + failedStep + ": " + applyErr.Error()
		if rollbackErr != nil {
			m.status.Degraded = true
			m.status.Error += "; rollback failed: " + rollbackErr.Error()
		} else {
			_ = os.Remove(filepath.Join(m.root, "pending.json"))
		}
		_ = m.saveJSON("manifest.json", applyManifest{Status: m.status, Paths: m.paths, Domains: m.domains})
		return "", "", fmt.Errorf("%w: %s", ErrFailed, m.status.Error)
	}
	m.status = ApplyStatus{Desired: generation, Applied: generation, AppliedAt: time.Now().UTC(), Ready: true, Step: "ready"}
	m.paths = paths
	m.domains = nil
	for _, d := range p.Postfix.Domains {
		m.domains = append(m.domains, d.FQDN)
	}
	if err := m.saveJSON("manifest.json", applyManifest{Status: m.status, Paths: paths, Domains: m.domains}); err != nil {
		m.status.Ready = false
		m.status.Degraded = true
		return "", "", err
	}
	if err := os.Remove(filepath.Join(m.root, "pending.json")); err != nil {
		return "", "", err
	}
	if m.automaticCertificates && m.certificatePending.CompareAndSwap(false, true) {
		go func() { defer m.certificatePending.Store(false); _, _, _ = m.RenewCertificates(json.RawMessage(`{}`)) }()
	}
	return generation, "", nil
}

// ConfigUnits defines the fixed restart order for generated configuration.
var ConfigUnits = []string{"opendkim", "dovecot", "postfix", "nginx", "nsd"}

func preservedExternalPath(cfg *config.Config, path string) bool {
	if cfg == nil {
		return false
	}
	switch {
	case cfg.PreserveExternalNSD && (strings.HasPrefix(path, "/etc/nsd/") || path == "/etc/nsd"):
		return true
	case cfg.PreserveExternalWeb && (strings.HasPrefix(path, "/etc/nginx/") || path == "/etc/nginx"):
		return true
	case cfg.PreserveExternalOpenDKIM && (strings.HasPrefix(path, "/etc/opendkim") || strings.Contains(path, "/opendkim/")):
		return true
	default:
		return false
	}
}

func filterPreservedExternalFiles(cfg *config.Config, files map[string]string) map[string]string {
	if cfg == nil || (!cfg.PreserveExternalNSD && !cfg.PreserveExternalWeb && !cfg.PreserveExternalOpenDKIM) {
		return files
	}
	filtered := make(map[string]string, len(files))
	for path, content := range files {
		if preservedExternalPath(cfg, path) {
			continue
		}
		filtered[path] = content
	}
	return filtered
}

func configUnits(cfg *config.Config) []string {
	units := make([]string, 0, len(ConfigUnits))
	for _, unit := range ConfigUnits {
		if cfg != nil {
			if cfg.PreserveExternalNSD && unit == "nsd" {
				continue
			}
			if cfg.PreserveExternalWeb && unit == "nginx" {
				continue
			}
			if cfg.PreserveExternalOpenDKIM && unit == "opendkim" {
				continue
			}
		}
		units = append(units, unit)
	}
	return units
}

func (m *ApplyManager) render(p ApplyParams) (map[string]string, error) {
	if len(p.Postfix.Domains) > 10000 || len(p.Postfix.Mailboxes) > 100000 || len(p.Postfix.Aliases) > 100000 || len(p.DNS.Zones) > 10000 || len(p.DKIM.Domains) > 10000 {
		return nil, fmt.Errorf("snapshot exceeds limits")
	}
	cfg := m.cfg
	host := cfg.MyHostname
	if host == "" {
		host = "box." + cfg.PrimaryDomain
	}
	domains := map[string]bool{}
	for _, d := range p.Postfix.Domains {
		domains[d.FQDN] = true
	}
	for _, d := range p.DKIM.Domains {
		if !domains[d.Domain] || len(d.PrivateKey) > 32768 {
			return nil, fmt.Errorf("invalid DKIM domain")
		}
		if _, err := domainmail.PublicKeyTXT(d.PrivateKey); err != nil {
			return nil, fmt.Errorf("invalid DKIM key")
		}
	}
	for _, z := range p.DNS.Zones {
		if !domains[z.Domain] || len(z.Records) > 1000 {
			return nil, fmt.Errorf("invalid DNS domain")
		}
		for _, r := range z.Records {
			if len(r.Value) > 16384 {
				return nil, fmt.Errorf("record exceeds limit")
			}
		}
	}
	cert := filepath.Join(m.root, "certs/mail", host+".cert")
	key := filepath.Join(m.root, "certs/mail", host+".key")
	mailContext := mail.ConfigContext{
		PostfixNode: postfix.NodeConfig{Hostname: host, PrimaryDomain: cfg.PrimaryDomain, StorageRoot: cfg.StorageRoot, TLSCertFile: cert, TLSKeyFile: key, PolicyEnabled: cfg.PolicyEnabled, V80Preserve: cfg.PostfixV80Preserve, PreserveExternalOpenDKIM: cfg.PreserveExternalOpenDKIM, PolicySocket: cfg.PolicySocket, ArchiveEnabled: cfg.ArchiveEnabled}, PostfixSnap: p.Postfix,
		DovecotNode: dovecot.NodeConfig{Hostname: host, PrimaryDomain: cfg.PrimaryDomain, StorageRoot: cfg.StorageRoot, TLSCertFile: cert, TLSKeyFile: key, DatabaseURL: cfg.DatabaseURLDovecot, V80Preserve: cfg.PostfixV80Preserve},
		NSDNode:     nsd.NodeConfig{ListenAddresses: cfg.DNSListenAddresses, Hostname: host, PrimaryDomain: cfg.PrimaryDomain, PublicIPv4: cfg.PublicIPv4, Nameservers: cfg.Nameservers}, NSDSnap: p.DNS,
		NginxNode: nginx.NodeConfig{PrimaryDomain: cfg.PrimaryDomain, StorageRoot: cfg.StorageRoot, BackendHTTP: "http://" + cfg.ListenAddr, WebmailBackend: fixedWebmailBackend(cfg)}, DKIM: p.DKIM,
	}
	// An explicit panel hostname wins over the distribution's default HTTP
	// server, so HTTP-01 never falls through to /var/www/html.
	panelPaths := m.certificatePaths(host)
	mailContext.NginxSnap.Domains = append(mailContext.NginxSnap.Domains, nginx.DomainConfig{FQDN: host, TLSCertFile: certificateCertPath(panelPaths), TLSKeyFile: certificateKeyPath(panelPaths), EnableWebmail: cfg.RoundcubeEnabled})
	for _, d := range p.Postfix.Domains {
		if !m.customWebEnabled(d.FQDN) {
			continue
		}
		if d.FQDN == host {
			continue
		}
		c := filepath.Join(m.root, "certs/domains", d.FQDN+".cert")
		k := filepath.Join(m.root, "certs/domains", d.FQDN+".key")
		if _, err := os.Stat(c); err == nil {
			if _, err := os.Stat(k); err == nil {
				mailContext.NginxSnap.Domains = append(mailContext.NginxSnap.Domains, nginx.DomainConfig{FQDN: d.FQDN, TLSCertFile: c, TLSKeyFile: k, EnableWebmail: true})
			}
		}
	}
	if m.versions != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reconcile.ReserveDNSVersions(ctx, m.versions, mailContext.NSDNode, &mailContext.NSDSnap); err != nil {
			return nil, err
		}
	}
	return mail.RenderAll(mailContext)
}

func runtimeMode(path string) os.FileMode {
	if mail.SecretFile(path) {
		return 0640
	}
	return 0644
}
func (m *ApplyManager) write(path string, data []byte, mode os.FileMode) error {
	roots := allowedWriteRoots
	path = m.target(path)
	if m.targetRoot != "" {
		roots = []string{m.targetRoot}
	}
	writer := m.fileWriter
	if writer == nil {
		writer = writeFileAtomic
	}
	if _, err := writer(path, data, mode, roots); err != nil {
		return err
	}
	if strings.HasSuffix(path, "db.conf") {
		if err := chownGroup(path, "dovecot"); err != nil {
			return err
		}
		if err := chownGroup(filepath.Dir(path), "dovecot"); err != nil {
			return err
		}
		return os.Chmod(filepath.Dir(path), 0750)
	}
	if strings.HasSuffix(path, ".private") {
		if err := chownGroup(path, "opendkim"); err != nil {
			return err
		}
		if err := chownGroup(filepath.Dir(path), "opendkim"); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(path), 0750); err != nil {
			return err
		}
		if err := chownGroup(filepath.Dir(filepath.Dir(path)), "opendkim"); err != nil {
			return err
		}
		return os.Chmod(filepath.Dir(filepath.Dir(path)), 0750)
	}
	if strings.HasPrefix(path, m.target("/etc/postfix/urmail/")) || strings.HasPrefix(path, m.target("/etc/nsd/zones/")) {
		return os.Chmod(filepath.Dir(path), 0755)
	}
	return nil
}
func (m *ApplyManager) check(ctx context.Context, p ApplyParams) error {
	checks := [][]string{{"/usr/sbin/opendkim", "-n", "-x", opendkim.ConfPath}, {"/usr/bin/doveconf", "-n"}, {"/usr/sbin/postfix", "check"}, {"/usr/sbin/nginx", "-t"}, {"/usr/sbin/nsd-checkconf", "/etc/nsd/nsd.conf"}}
	for _, c := range checks {
		name := filepath.Base(c[0])
		if m.cfg != nil {
			if m.cfg.PreserveExternalOpenDKIM && name == "opendkim" {
				continue
			}
			if m.cfg.PreserveExternalWeb && name == "nginx" {
				continue
			}
			if m.cfg.PreserveExternalNSD && name == "nsd-checkconf" {
				continue
			}
		}
		m.status.Step = "check:" + name
		m.publish()
		if err := m.run(ctx, c[0], c[1:]...); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
	}
	if m.cfg != nil && m.cfg.PreserveExternalNSD {
		return nil
	}
	for _, z := range p.DNS.Zones {
		if err := m.run(ctx, "/usr/sbin/nsd-checkzone", z.Domain, "/etc/nsd/zones/"+z.Domain+".txt"); err != nil {
			return fmt.Errorf("check zone: %w", err)
		}
	}
	return nil
}
func (m *ApplyManager) restore(j applyJournal) error {
	for path, s := range j.Files {
		if s.Exists && m.versions != nil && strings.HasPrefix(path, "/etc/nsd/zones/") && strings.HasSuffix(path, ".txt") {
			re := regexp.MustCompile(`(?m)^    ([0-9]+) ; serial$`)
			match := re.FindSubmatch(s.Content)
			if len(match) == 2 {
				seed, err := strconv.ParseUint(string(match[1]), 10, 32)
				if err != nil {
					return err
				}
				normalized := re.ReplaceAll(s.Content, []byte("    0 ; serial"))
				sum := sha256.Sum256(normalized)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				serial, err := m.versions.ReserveZoneSerial(ctx, strings.TrimSuffix(filepath.Base(path), ".txt"), hex.EncodeToString(sum[:]), uint32(seed)) // #nosec G115 -- ParseUint above enforces 32 bits.
				cancel()
				if err != nil {
					return err
				}
				s.Content = re.ReplaceAll(s.Content, []byte(fmt.Sprintf("    %d ; serial", serial)))
			}
		}
		if s.Exists {
			if err := m.write(path, s.Content, os.FileMode(s.Mode)); err != nil {
				return err
			}
			if os.Geteuid() == 0 {
				if err := os.Chown(m.target(path), s.UID, s.GID); err != nil {
					return err
				}
			}
		} else if err := os.Remove(m.target(path)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, unit := range j.Reloaded {
		if err := m.run(ctx, "/usr/bin/systemctl", "restart", unit); err != nil {
			return fmt.Errorf("rollback %s: %w", unit, err)
		}
	}
	return nil
}
func (m *ApplyManager) recover() error {
	raw, err := os.ReadFile(filepath.Join(m.root, "pending.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j applyJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if err := m.restore(j); err != nil {
		return fmt.Errorf("recover interrupted configuration: %w", err)
	}
	m.status = j.Previous
	m.paths = j.PreviousPaths
	m.domains = j.PreviousDomains
	m.status.Ready = false
	m.status.Step = "recovered"
	if err := m.saveJSON("manifest.json", applyManifest{Status: m.status, Paths: m.paths, Domains: m.domains}); err != nil {
		return err
	}
	return os.Remove(filepath.Join(m.root, "pending.json"))
}
func (m *ApplyManager) saveJSON(name string, v any) error {
	if name == "manifest.json" {
		m.publish()
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.root, ".state-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer func() { _ = os.Remove(m.target(path)) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(path, filepath.Join(m.root, name))
	}
	if err == nil {
		err = syncDir(m.root)
	}
	return err
}
func saveFile(path string) (savedFile, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return savedFile{}, nil
	}
	if err != nil {
		return savedFile{}, err
	}
	if !info.Mode().IsRegular() {
		return savedFile{}, fmt.Errorf("unsafe configuration target %s", path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is a canonical managed file or a root-created certificate consumer.
	uid, gid := fileOwner(info)
	return savedFile{Content: raw, Mode: uint32(info.Mode().Perm()), Exists: true, UID: uid, GID: gid}, err
}
func runCommand(ctx context.Context, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- only fixed absolute daemon commands call this helper.
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/root"}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(path), err)
	}
	return nil
}

func (m *ApplyManager) target(path string) string {
	if m.targetRoot == "" {
		return path
	}
	return filepath.Join(m.targetRoot, strings.TrimPrefix(path, "/"))
}

func (m *ApplyManager) publish() { m.published.Store(m.status) }

// Close releases the trusted platform database connection.
func (m *ApplyManager) Close() {
	if m.pool != nil {
		m.pool.Close()
	}
}

func containsUnit(units []string, target string) bool {
	for _, unit := range units {
		if unit == target {
			return true
		}
	}
	return false
}

func fixedWebmailBackend(cfg *config.Config) string {
	if cfg.RoundcubeEnabled {
		return "http://127.0.0.1:8888"
	}
	return ""
}

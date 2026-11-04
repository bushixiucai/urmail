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
type ApplyParams = reconcile.ApplyParams

// BuildApplyParams forwards to reconcile.BuildApplyParams for agent callers.
var BuildApplyParams = reconcile.BuildApplyParams

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

// ApplyJournalVersion defines the journal format version.
const ApplyJournalVersion = 2

// DNSRollbackItem holds typed rollback data for a DNS domain.
type DNSRollbackItem struct {
	Domain        string       `json:"domain"`
	OldSigned     savedFile    `json:"old_signed"`
	OldTxt        savedFile    `json:"old_txt"`
	StateSnapshot *dnsKeyState `json:"state_snapshot,omitempty"`
	SignMode      bool         `json:"sign_mode"`
	ZoneMode      string       `json:"zone_mode,omitempty"`
}

type applyJournal struct {
	Version         int                        `json:"version"`
	Files           map[string]savedFile       `json:"files"`
	DNS             map[string]DNSRollbackItem `json:"dns,omitempty"`
	Previous        ApplyStatus                `json:"previous"`
	PreviousPaths   []string                   `json:"previous_paths"`
	PreviousDomains []string                   `json:"previous_domains"`
	Reloaded        []string                   `json:"reloaded"`
}

// ManifestFileRecord captures file integrity metadata for drift detection and self-healing.
type ManifestFileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
}

const appliedManifestBackupPath = "/var/lib/urmail-backup/applied_manifest.json"

type applyManifest struct {
	Status  ApplyStatus                   `json:"status"`
	Paths   []string                      `json:"paths"`
	Domains []string                      `json:"domains"`
	Files   map[string]ManifestFileRecord `json:"files,omitempty"`
}

// CommandRunner executes a fixed privileged command with a deadline.
type CommandRunner func(context.Context, string, ...string) error

// ApplyManager is the only production writer of mail service configuration.
// All transactions are serialized, and its state directory is root-owned.
type ApplyManager struct {
	mu                    sync.Mutex
	dnsMu                 sync.Mutex
	certMu                sync.Mutex
	brandMu               sync.Mutex
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
	transportDomains      []string
	targetRoot            string
	webroot               string
	automaticCertificates bool
	certificatePending    atomic.Bool
	// certRenewBackoff implements the MAIL-006 bounded renewal backoff.
	// Guarded by certMu; zero value means "eligible to try now".
	certRenewFails   int
	certRenewNextTry time.Time
	fileWriter       func(string, []byte, os.FileMode, []string) (string, error)
	manifestFiles    map[string]ManifestFileRecord
	onDrift          func(string)
	prepareOnly      bool
}

// Target returns the target filesystem path for a simulated system file.
func (m *ApplyManager) Target(path string) string {
	return m.target(path)
}

// NewTestApplyManager constructs an ApplyManager rooted in the provided temporary directory,
// simulating external service commands and tools (such as sievec and postmap) for deterministic testing.
func NewTestApplyManager(tempDir string, cfg *config.Config) (*ApplyManager, error) {
	root, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		return nil, err
	}
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{
			MyHostname:         "box.example.com",
			PrimaryDomain:      "example.com",
			StorageRoot:        filepath.Join(root, "mail"),
			DatabaseURLDovecot: "host=127.0.0.1 user=dovecot password=secret",
			ListenAddr:         "127.0.0.1:8080",
			PublicIPv4:         "192.0.2.1",
			Nameservers:        []string{"ns.example.com"},
		}
	}
	m := &ApplyManager{
		root:           state,
		targetRoot:     filepath.Join(root, "system"),
		webroot:        filepath.Join(root, "acme"),
		cfg:            cfg,
		fileWriter:     writeFileAtomic,
		readinessProbe: func(context.Context) error { return nil },
	}
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/sievec" && len(args) == 6 && args[0] == "-P" && args[1] == "sieve_extprograms" && args[2] == "-x" && args[3] == "+vnd.dovecot.execute" {
			args = args[4:]
			source, err := os.ReadFile(m.target(args[0]))
			if err != nil {
				return err
			}
			return os.WriteFile(m.target(args[1]), append([]byte("compiled sieve: "), source...), 0640) // #nosec G703 -- simulated test tool writes only to isolated targetRoot.
		}
		if path == "/usr/sbin/postmap" && len(args) == 1 {
			return os.WriteFile(m.target(args[0]+".db"), []byte("compiled map"), 0644) // #nosec G703 -- simulated test tool writes only to isolated targetRoot.
		}
		return nil
	}
	var testManifest applyManifest
	if raw, err := os.ReadFile(filepath.Join(m.root, "manifest.json")); err == nil {
		if err = json.Unmarshal(raw, &testManifest); err == nil {
			m.status = testManifest.Status
			m.paths = testManifest.Paths
			m.domains = testManifest.Domains
			m.manifestFiles = testManifest.Files
		}
	} else if raw, err := os.ReadFile(m.target(appliedManifestBackupPath)); err == nil {
		if err = json.Unmarshal(raw, &testManifest); err == nil {
			m.status = testManifest.Status
			m.paths = testManifest.Paths
			m.domains = testManifest.Domains
			m.manifestFiles = testManifest.Files
		}
	}
	m.publish()
	return m, nil
}

// NewApplyManager creates the serialized root configuration controller.
func NewApplyManager(cfg *config.Config) (*ApplyManager, error) {
	m := &ApplyManager{cfg: cfg, root: StateRoot, run: runCommand, automaticCertificates: true, fileWriter: writeFileAtomic}
	if cfg.DNSSecondaryOnly {
		if err := os.MkdirAll(m.root, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(m.root, 0700); err != nil {
			return nil, err
		}
		return m, nil
	}
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
		m.manifestFiles = manifest.Files
	} else if raw, err := os.ReadFile(m.target(appliedManifestBackupPath)); err == nil {
		if err = json.Unmarshal(raw, &manifest); err == nil {
			m.status = manifest.Status
			m.status.Ready = false
			m.status.Step = "startup"
			m.paths = manifest.Paths
			m.domains = manifest.Domains
			m.manifestFiles = manifest.Files
		}
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
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); err == nil {
		m.status.Ready = false
		m.status.Degraded = true
		m.status.Error = "cannot apply: pending uncommitted journal exists"
		m.publish()
		return "", "", fmt.Errorf("%w: pending apply journal exists; manual resolution or recovery required", ErrRejected)
	}
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
	if err := m.prepareMailStorage(); err != nil {
		m.status.Step = "mail-storage"
		m.status.Error = "mail storage preparation failed: " + err.Error()
		return "", "", err
	}
	files = filterPreservedExternalFiles(m.cfg, files)
	_, brandManaged := files[brandManagedNginxPath]
	retainedPaths := []string{}
	for _, path := range m.paths {
		if preservedExternalPath(m.cfg, path) {
			retainedPaths = append(retainedPaths, path)
		}
	}
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
	if !m.prepareOnly && m.status.Ready && m.status.Applied == generation {
		driftDetected, driftReason := m.detectDiskDrift()
		if driftDetected {
			m.status.Ready = false
			m.status.Step = "drift-detected"
			m.status.Error = "disk drift detected: " + driftReason
			m.publish()
			if m.onDrift != nil {
				m.onDrift(driftReason)
			}
		} else if m.dnsDerivedCurrent(p.DNS) {
			// Keep Ready=true while rechecking so concurrent config.status readers
			// do not flap on probes during an otherwise healthy generation.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := m.checkRuntimeReadiness(ctx); err != nil {
				m.status.Ready = false
				m.status.Step = "runtime-recovery"
				m.status.Error = "configuration readiness failed at " + m.status.Step + ": " + err.Error()
				m.publish()
				if m.onDrift != nil {
					m.onDrift(m.status.Error)
				}
			} else {
				m.status.Step = "ready"
				m.status.Error = ""
				m.status.Ready = true
				m.publish()
				return generation, "", nil
			}
		}
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
	journal := applyJournal{
		Version:         ApplyJournalVersion,
		Files:           map[string]savedFile{},
		DNS:             map[string]DNSRollbackItem{},
		Previous:        previous,
		PreviousPaths:   append([]string{}, m.paths...),
		PreviousDomains: append([]string{}, m.domains...),
	}
	targets := append(append([]string{}, m.paths...), paths...)
	for _, path := range append(append([]string{}, m.paths...), paths...) {
		if strings.HasSuffix(path, ".map") {
			targets = append(targets, path+".db")
		}
		if strings.HasSuffix(path, "/urmail-spam/spam-before.sieve") || strings.HasSuffix(path, ".sieve") {
			targets = append(targets, strings.TrimSuffix(path, ".sieve")+".svbin")
		}
	}
	targets = append(targets, "/etc/dovecot/urmail-spam/spam-before.svbin")
	if _, err := os.Stat(m.target("/etc/postfix/aliases.db")); err == nil {
		targets = append(targets, "/etc/postfix/aliases.db")
	}
	if _, err := os.Stat(m.target("/etc/postfix/aliases")); err == nil {
		targets = append(targets, "/etc/postfix/aliases")
	}
	if _, err := os.Stat(m.target(dnsTSIGPath)); err == nil {
		targets = append(targets, dnsTSIGPath)
	}
	targets = append(targets, appliedManifestBackupPath)
	for _, path := range targets {
		if _, ok := journal.Files[path]; ok {
			continue
		}
		m.status.Step = "stage:" + path
		roots := allowedWriteRoots
		if path == brandManagedNginxPath {
			roots = []string{filepath.Dir(brandManagedNginxPath)}
		}
		if _, _, err := resolveAllowedPath(path, roots); err != nil {
			return "", "", err
		}
		saved, err := saveFile(m.target(path))
		if err != nil {
			return "", "", err
		}
		journal.Files[path] = saved
	}

	dnsDomains := map[string]struct{}{}
	for _, z := range p.DNS.Zones {
		dnsDomains[z.Domain] = struct{}{}
	}
	for _, path := range m.paths {
		if strings.HasPrefix(path, "/etc/nsd/zones/") {
			base := strings.TrimPrefix(path, "/etc/nsd/zones/")
			if strings.HasSuffix(base, ".txt") {
				dnsDomains[strings.TrimSuffix(base, ".txt")] = struct{}{}
			} else if strings.HasSuffix(base, ".signed") {
				dnsDomains[strings.TrimSuffix(base, ".signed")] = struct{}{}
			}
		}
	}
	for domain := range dnsDomains {
		signedPath := "/etc/nsd/zones/" + domain + ".signed"
		txtPath := "/etc/nsd/zones/" + domain + ".txt"
		oldSigned, err := saveFile(m.target(signedPath))
		if err != nil {
			return "", "", err
		}
		oldTxt, err := saveFile(m.target(txtPath))
		if err != nil {
			return "", "", err
		}
		var stateSnap *dnsKeyState
		if state, err := m.loadDNSState(domain); err == nil {
			copyState := state
			stateSnap = &copyState
		}
		var zoneMode string
		for _, z := range p.DNS.Zones {
			if z.Domain == domain {
				zoneMode = z.Mode
				break
			}
		}
		signMode := oldSigned.Exists || (stateSnap != nil && (stateSnap.Phase == "signed" || stateSnap.Phase == "dual-signing"))
		journal.DNS[domain] = DNSRollbackItem{
			Domain:        domain,
			OldSigned:     oldSigned,
			OldTxt:        oldTxt,
			StateSnapshot: stateSnap,
			SignMode:      signMode,
			ZoneMode:      zoneMode,
		}
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
			if strings.HasSuffix(path, ".signed") && files[path] == "" {
				// Derived DNSSEC signed zone is populated exclusively by staging signDNSZone.
				continue
			}
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
		m.status.Step = "compile:dns-security"
		if err := m.installDNSDerived(ctx, p.DNS); err != nil {
			return err
		}
		m.status.Step = "compile:sieve"
		m.publish()
		if err := m.compileSpamScript(ctx); err != nil {
			return err
		}
		m.status.Step = "check"
		m.publish()
		if brandManaged {
			if err := m.run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
				return err
			}
			if err := m.verifyHybridBrandInclude(ctx); err != nil {
				return err
			}
		}
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
		postmapTargets := make([]string, 0, len(paths)+1)
		for _, path := range paths {
			if strings.HasSuffix(path, ".map") {
				postmapTargets = append(postmapTargets, path)
			}
		}
		if _, err := os.Stat(m.target("/etc/postfix/aliases")); err == nil {
			postmapTargets = append(postmapTargets, "/etc/postfix/aliases")
		}
		for _, path := range postmapTargets {
			if err := m.run(ctx, "/usr/sbin/postmap", path); err != nil {
				return err
			}
			if err := os.Chmod(m.target(path+".db"), 0644); err != nil {
				return err
			}
		}
		if m.prepareOnly {
			return nil
		}
		m.status.Step = "reload"
		m.publish()
		units := configUnits(m.cfg)
		if brandManaged {
			units = append(units, "nginx")
		}
		for _, unit := range units {
			m.status.Step = "reload:" + unit
			m.publish()
			if !containsUnit(journal.Reloaded, unit) {
				journal.Reloaded = append(journal.Reloaded, unit)
			}
			if err := m.saveJSON("pending.json", journal); err != nil {
				return err
			}
			verb := "restart"
			if unit == "nginx" && brandManaged {
				verb = "reload"
			}
			if err := m.run(ctx, "/usr/bin/systemctl", verb, unit); err != nil {
				return fmt.Errorf("reload %s: %w", unit, err)
			}
		}
		if err := m.checkRuntimeReadiness(ctx); err != nil {
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
		_ = m.saveJSON("manifest.json", applyManifest{Status: m.status, Paths: m.paths, Domains: m.domains, Files: m.manifestFiles})
		return "", "", fmt.Errorf("%w: %s", ErrFailed, m.status.Error)
	}
	if m.prepareOnly {
		m.status = ApplyStatus{Desired: generation, Applied: previous.Applied, AppliedAt: previous.AppliedAt, Ready: false, Step: "prepared"}
	} else {
		m.status = ApplyStatus{Desired: generation, Applied: generation, AppliedAt: time.Now().UTC(), Ready: true, Step: "ready"}
	}
	m.paths = paths
	m.paths = append(m.paths, retainedPaths...)
	sort.Strings(m.paths)
	m.domains = nil
	for _, d := range p.Postfix.Domains {
		m.domains = append(m.domains, d.FQDN)
	}
	m.domains = append(m.domains, m.transportDomains...)
	var colErr error
	m.manifestFiles, colErr = m.collectManifestFiles(m.paths, p.DNS)
	if colErr != nil {
		m.status.Ready = false
		m.status.Degraded = true
		m.status.Error = "record manifest files: " + colErr.Error()
		return "", "", colErr
	}
	if err := m.saveCurrentManifest(); err != nil {
		m.status.Ready = false
		m.status.Degraded = true
		return "", "", err
	}
	if err := os.Remove(filepath.Join(m.root, "pending.json")); err != nil {
		return "", "", err
	}
	if !m.prepareOnly && m.automaticCertificates && m.cfg.BackgroundWorkersAllowed() && m.certificatePending.CompareAndSwap(false, true) {
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
	case (cfg.PreserveExternalNSD || cfg.WorkersHeld) && (strings.HasPrefix(path, "/etc/nsd/") || path == "/etc/nsd"):
		return true
	case cfg.PreserveExternalWeb && path == brandManagedNginxPath:
		return false
	case cfg.PreserveExternalWeb && (strings.HasPrefix(path, "/etc/nginx/") || path == "/etc/nginx"):
		return true
	case cfg.PreserveExternalOpenDKIM && (strings.HasPrefix(path, "/etc/opendkim") || strings.Contains(path, "/opendkim/")):
		return true
	default:
		return false
	}
}

func filterPreservedExternalFiles(cfg *config.Config, files map[string]string) map[string]string {
	if cfg == nil || (!cfg.WorkersHeld && !cfg.PreserveExternalNSD && !cfg.PreserveExternalWeb && !cfg.PreserveExternalOpenDKIM) {
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
			if (cfg.PreserveExternalNSD || cfg.WorkersHeld) && unit == "nsd" {
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
	if err := m.enrichDNSSnapshot(&p.DNS); err != nil {
		return nil, err
	}
	if len(p.SpamPolicies) > 10000 || len(p.Postfix.Domains) > 10000 || len(p.Postfix.Mailboxes) > 100000 || len(p.Postfix.Aliases) > 100000 || len(p.DNS.Zones) > 10000 || len(p.DKIM.Domains) > 10000 {
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
	activeMailboxes := map[string]bool{}
	for _, mb := range p.Postfix.Mailboxes {
		if mb.Status == "active" {
			activeMailboxes[strings.ToLower(mb.Address)] = true
		}
	}
	for _, policy := range p.SpamPolicies {
		if !activeMailboxes[strings.ToLower(policy.MailboxAddress)] {
			return nil, fmt.Errorf("spam policy mailbox outside active snapshot")
		}
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
		DovecotNode: dovecot.NodeConfig{SpamPolicies: p.SpamPolicies, Hostname: host, PrimaryDomain: cfg.PrimaryDomain, StorageRoot: cfg.StorageRoot, TLSCertFile: cert, TLSKeyFile: key, DatabaseURL: cfg.DatabaseURLDovecot, V80Preserve: cfg.PostfixV80Preserve},
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
	mailContext.NginxSnap.Domains = append(mailContext.NginxSnap.Domains, m.transportNginxDomains()...)
	brandDomains, brandErr := m.brandNginxDomains()
	if brandErr != nil {
		return nil, brandErr
	}
	mailContext.NginxSnap.Domains = append(mailContext.NginxSnap.Domains, brandDomains...)
	if m.versions != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reconcile.ReserveDNSVersions(ctx, m.versions, mailContext.NSDNode, &mailContext.NSDSnap); err != nil {
			return nil, err
		}
	}
	files, err := mail.RenderAll(mailContext)
	if err != nil {
		return nil, err
	}
	if cfg.PreserveExternalWeb {
		hybrid, hybridErr := m.hybridBrandFiles(brandDomains)
		if hybridErr != nil {
			return nil, hybridErr
		}
		for path, content := range hybrid {
			files[path] = content
		}
	}
	if err = m.addDNSSecureFiles(files, mailContext.NSDSnap); err != nil {
		return nil, err
	}
	return files, nil
}

func runtimeMode(path string) os.FileMode {
	if strings.HasSuffix(path, "/urmail-spam/execute/outbound-check") {
		return 0755
	}
	if mail.SecretFile(path) {
		return 0640
	}
	return 0644
}
func (m *ApplyManager) write(path string, data []byte, mode os.FileMode) error {
	roots := allowedWriteRoots
	// This single additive include is a typed capability, never a new general
	// /etc/nginx/conf.d write root for other agent operations.
	if path == brandManagedNginxPath {
		roots = []string{filepath.Dir(m.target(brandManagedNginxPath))}
	}
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
	if strings.HasSuffix(path, "/urmail-spam/execute/outbound-check") {
		if os.Geteuid() == 0 {
			if err := os.Chown(path, 0, 0); err != nil {
				return err
			}
			if err := os.Chown(filepath.Dir(path), 0, 0); err != nil {
				return err
			}
		}
		return os.Chmod(filepath.Dir(path), 0755)
	}
	if strings.HasSuffix(path, "/urmail-spam/spam-before.sieve") || strings.HasSuffix(path, "/urmail-spam/spam-before.svbin") {
		if os.Geteuid() == 0 {
			if err := chownGroup(path, "mail"); err != nil {
				return err
			}
			if err := chownGroup(filepath.Dir(path), "mail"); err != nil {
				return err
			}
		}
		return os.Chmod(filepath.Dir(path), 0750)
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

// spamBinaryCurrent repairs a missing/stale derived artifact even when the
// canonical source generation did not change during an agent upgrade.
func (m *ApplyManager) spamBinaryCurrent() bool {
	source, err := os.Stat(m.target("/etc/dovecot/urmail-spam/spam-before.sieve"))
	if err != nil || !source.Mode().IsRegular() {
		return false
	}
	binary, err := os.Stat(m.target("/etc/dovecot/urmail-spam/spam-before.svbin"))
	return err == nil && binary.Mode().IsRegular() && binary.Mode().Perm() == 0640 && !binary.ModTime().Before(source.ModTime())
}

// compileSpamScript keeps the private global source read-only for mail workers.
// The compiled artifact participates in the same rollback journal as its source.
func (m *ApplyManager) compileSpamScript(ctx context.Context) error {
	const source = "/etc/dovecot/urmail-spam/spam-before.sieve"
	const binary = "/etc/dovecot/urmail-spam/spam-before.svbin"
	if err := m.run(ctx, "/usr/bin/sievec", "-P", "sieve_extprograms", "-x", "+vnd.dovecot.execute", source, binary); err != nil {
		return fmt.Errorf("compile private spam sieve: %w", err)
	}
	if err := os.Chmod(m.target(binary), 0640); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return chownGroup(m.target(binary), "mail")
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
			if (m.cfg.PreserveExternalNSD || m.cfg.WorkersHeld) && name == "nsd-checkconf" {
				continue
			}
		}
		m.status.Step = "check:" + name
		m.publish()
		if err := m.run(ctx, c[0], c[1:]...); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
	}
	if m.cfg != nil && (m.cfg.PreserveExternalNSD || m.cfg.WorkersHeld) {
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
	// If legacy version 1 journal has DNS files, fail closed because it lacks typed DNS context
	if j.Version < ApplyJournalVersion {
		for path := range j.Files {
			if strings.HasPrefix(path, "/etc/nsd/zones/") {
				return fmt.Errorf("legacy journal version %d missing complete DNSSEC rollback context for %s", j.Version, path)
			}
		}
	}

	for path, s := range j.Files {
		if strings.HasPrefix(path, "/etc/nsd/zones/") && (strings.HasSuffix(path, ".txt") || strings.HasSuffix(path, ".signed")) {
			if len(j.DNS) > 0 {
				continue
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

	dnsResigned := false
	for domain, item := range j.DNS {
		wasSigned := item.OldSigned.Exists || item.SignMode
		if item.OldTxt.Exists {
			content := item.OldTxt.Content
			re := regexp.MustCompile(`(?m)^\s*([0-9]+)\s*;\s*serial`)
			match := re.FindSubmatch(content)
			if len(match) == 2 {
				seed, err := strconv.ParseUint(string(match[1]), 10, 32)
				if err != nil {
					return fmt.Errorf("parse serial for %s: %w", domain, err)
				}
				normalized := re.ReplaceAll(content, []byte("    0 ; serial"))
				sum := sha256.Sum256(normalized)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				var serial uint32
				if m.versions != nil {
					serial, err = m.versions.ReserveZoneSerial(ctx, domain, hex.EncodeToString(sum[:]), uint32(seed))
				} else {
					serial = uint32((int64(seed) + 1) % 4294967296)
				}
				cancel()
				if err != nil {
					return fmt.Errorf("reserve zone serial for %s rollback: %w", domain, err)
				}
				content = re.ReplaceAll(content, []byte(fmt.Sprintf("    %d ; serial", serial)))
			}
			if err := m.write("/etc/nsd/zones/"+domain+".txt", content, 0644); err != nil {
				return fmt.Errorf("write restored txt zone for %s: %w", domain, err)
			}
		} else {
			if err := os.Remove(m.target("/etc/nsd/zones/" + domain + ".txt")); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove txt zone for %s: %w", domain, err)
			}
		}

		// Restore DNS state snapshot
		if item.StateSnapshot != nil {
			if err := m.saveDNSState(domain, *item.StateSnapshot); err != nil {
				return fmt.Errorf("restore DNS state for %s: %w", domain, err)
			}
		} else {
			_ = os.Remove(filepath.Join(m.dnsDirectory(domain), "state.json"))
		}

		if wasSigned {
			// Must re-sign restored txt content with existing valid keys using staging signDNSZone.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := m.signDNSZone(ctx, domain)
			cancel()
			if err != nil {
				return fmt.Errorf("re-sign zone %s during rollback: %w", domain, err)
			}
			dnsResigned = true
		} else {
			// Unsigned zone: remove any .signed file so NSD never loads it
			if err := os.Remove(m.target("/etc/nsd/zones/" + domain + ".signed")); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove signed zone for %s: %w", domain, err)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Restore writes may change source/binary mtimes in either order. Compile
	// the restored source before reloading workers so they never need write access.
	if _, err := os.Stat(m.target("/etc/dovecot/urmail-spam/spam-before.sieve")); err == nil {
		if err := m.compileSpamScript(ctx); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if m.prepareOnly {
		return nil
	}
	reloaded := append([]string{}, j.Reloaded...)
	if dnsResigned && !containsUnit(reloaded, "nsd") && (m.cfg == nil || (!m.cfg.PreserveExternalNSD && !m.cfg.WorkersHeld)) {
		reloaded = append(reloaded, "nsd")
	}

	for _, unit := range reloaded {
		verb := "restart"
		if unit == "nginx" && m.cfg.PreserveExternalWeb {
			verb = "reload"
		}
		if err := m.run(ctx, "/usr/bin/systemctl", verb, unit); err != nil {
			return fmt.Errorf("rollback %s: %w", unit, err)
		}
	}
	return nil
}

func (m *ApplyManager) recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	if j.Version < ApplyJournalVersion {
		hasDNS := len(j.DNS) > 0
		if !hasDNS {
			for path := range j.Files {
				if strings.HasPrefix(path, "/etc/nsd/zones/") {
					hasDNS = true
					break
				}
			}
		}
		if hasDNS {
			m.status.Ready = false
			m.status.Degraded = true
			m.status.Error = "legacy journal version 1 lacks complete DNSSEC rollback context; manual resolution required"
			m.publish()
			return fmt.Errorf("%w: legacy journal version %d missing complete DNSSEC rollback context", ErrRejected, j.Version)
		}
	}
	if err := m.restore(j); err != nil {
		m.status.Ready = false
		m.status.Degraded = true
		m.status.Error = fmt.Sprintf("recover interrupted configuration: %v", err)
		m.publish()
		return fmt.Errorf("recover interrupted configuration: %w", err)
	}
	m.status = j.Previous
	m.paths = j.PreviousPaths
	m.domains = j.PreviousDomains
	m.status.Ready = false
	m.status.Step = "recovered"
	if err := m.saveCurrentManifest(); err != nil {
		return err
	}
	return os.Remove(filepath.Join(m.root, "pending.json"))
}

func (m *ApplyManager) recordFileManifest(path string) (ManifestFileRecord, error) {
	target := m.target(path)
	info, err := os.Lstat(target)
	if err != nil {
		return ManifestFileRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return ManifestFileRecord{}, fmt.Errorf("%s is not a regular file", path)
	}
	content, err := os.ReadFile(target) // #nosec G304 -- target is an internal trusted managed path.
	if err != nil {
		return ManifestFileRecord{}, err
	}
	hash := sha256.Sum256(content)
	uid, gid := fileOwner(info)
	return ManifestFileRecord{
		Path:   path,
		SHA256: hex.EncodeToString(hash[:]),
		Mode:   uint32(info.Mode().Perm()),
		UID:    uid,
		GID:    gid,
	}, nil
}

func (m *ApplyManager) collectManifestFiles(paths []string, dns nsd.Snapshot) (map[string]ManifestFileRecord, error) {
	files := make(map[string]ManifestFileRecord)
	allPaths := make(map[string]struct{})
	for _, p := range paths {
		allPaths[p] = struct{}{}
		if strings.HasSuffix(p, ".map") {
			allPaths[p+".db"] = struct{}{}
		}
		if strings.HasSuffix(p, "/urmail-spam/spam-before.sieve") {
			allPaths["/etc/dovecot/urmail-spam/spam-before.svbin"] = struct{}{}
		}
	}
	allPaths["/etc/dovecot/urmail-spam/spam-before.svbin"] = struct{}{}
	if _, err := os.Stat(m.target("/etc/postfix/aliases.db")); err == nil {
		allPaths["/etc/postfix/aliases.db"] = struct{}{}
	}
	if _, err := os.Stat(m.target("/etc/postfix/aliases")); err == nil {
		allPaths["/etc/postfix/aliases"] = struct{}{}
		allPaths["/etc/postfix/aliases.db"] = struct{}{}
	}
	if tsigContent, err := m.dnsTSIGContent(dns); err == nil && tsigContent != "" {
		allPaths[dnsTSIGPath] = struct{}{}
	}
	for _, zone := range dns.Zones {
		if zone.Sign {
			allPaths["/etc/nsd/zones/"+zone.Domain+".signed"] = struct{}{}
		}
	}
	sortedPaths := make([]string, 0, len(allPaths))
	for p := range allPaths {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)
	for _, p := range sortedPaths {
		rec, err := m.recordFileManifest(p)
		if err != nil {
			return nil, fmt.Errorf("record file manifest %s: %w", p, err)
		}
		files[p] = rec
	}
	return files, nil
}

func (m *ApplyManager) detectDiskDrift() (bool, string) {
	if len(m.manifestFiles) == 0 {
		return true, "legacy or missing manifest file integrity records"
	}
	sortedPaths := make([]string, 0, len(m.manifestFiles))
	for p := range m.manifestFiles {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)
	for _, path := range sortedPaths {
		expected := m.manifestFiles[path]
		target := m.target(path)
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				return true, fmt.Sprintf("managed file %s is missing", path)
			}
			return true, fmt.Sprintf("failed to lstat %s: %v", path, err)
		}
		if !info.Mode().IsRegular() {
			return true, fmt.Sprintf("managed file %s is not a regular file", path)
		}
		if uint32(info.Mode().Perm()) != expected.Mode {
			return true, fmt.Sprintf("managed file %s mode changed: expected %04o, got %04o", path, expected.Mode, info.Mode().Perm())
		}
		uid, gid := fileOwner(info)
		if expected.UID >= 0 && uid != expected.UID {
			return true, fmt.Sprintf("managed file %s uid changed: expected %d, got %d", path, expected.UID, uid)
		}
		if expected.GID >= 0 && gid != expected.GID {
			return true, fmt.Sprintf("managed file %s gid changed: expected %d, got %d", path, expected.GID, gid)
		}
		content, err := os.ReadFile(target) // #nosec G304 -- target is an internal trusted managed path.
		if err != nil {
			return true, fmt.Sprintf("failed to read %s: %v", path, err)
		}
		hash := sha256.Sum256(content)
		currentHash := hex.EncodeToString(hash[:])
		if currentHash != expected.SHA256 {
			return true, fmt.Sprintf("managed file %s content tampered: expected sha256 %s, got %s", path, expected.SHA256, currentHash)
		}
	}
	if !m.spamBinaryCurrent() {
		return true, "spam sieve binary is stale or missing"
	}
	return false, ""
}

// CheckDiskDrift checks whether any managed files on disk deviate from the recorded manifest.
func (m *ApplyManager) CheckDiskDrift() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.detectDiskDrift()
}

func (m *ApplyManager) saveCurrentManifest() error {
	m.publish()
	manifest := applyManifest{
		Status:  m.status,
		Paths:   m.paths,
		Domains: m.domains,
		Files:   m.manifestFiles,
	}
	if err := m.saveJSON("manifest.json", manifest); err != nil {
		return err
	}
	return m.writeBackupManifest(manifest)
}

func (m *ApplyManager) writeBackupManifest(manifest applyManifest) error {
	targetPath := m.target(appliedManifestBackupPath)
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create backup manifest directory: %w", err)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".applied_manifest-*")
	if err != nil {
		return err
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err = f.Chmod(0644); err == nil {
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
		err = os.Rename(tempPath, targetPath)
	}
	if err == nil {
		err = syncDir(dir)
	}
	return err
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

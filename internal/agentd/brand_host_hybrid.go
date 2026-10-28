package agentd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

const brandManagedNginxPath = "/etc/nginx/conf.d/urmail-brand-hosts.conf"
const brandManagedNginxMarker = "# urMail managed brand hosts v1\n"

func (m *ApplyManager) brandNginxOutput(ctx context.Context) ([]byte, error) {
	if m.output != nil {
		return m.output(ctx, "/usr/sbin/nginx", "-T")
	}
	command := exec.CommandContext(ctx, "/usr/sbin/nginx", "-T")
	var output dnsToolBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("existing nginx configuration cannot be inspected")
	}
	return output.Bytes(), nil
}

func nginxWithoutComments(raw string) string {
	var out strings.Builder
	var quote rune
	escaped, comment := false, false
	for _, ch := range raw {
		if comment {
			if ch == '\n' {
				comment = false
				out.WriteRune(ch)
			}
			continue
		}
		if escaped {
			out.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			out.WriteRune(ch)
			continue
		}
		if quote != 0 {
			out.WriteRune(ch)
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			out.WriteRune(ch)
			continue
		}
		if ch == '#' {
			comment = true
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}
func nginxServerNameMatches(name, host string) (bool, error) {
	name = strings.Trim(name, "\"'")
	if strings.HasPrefix(name, "~") {
		pattern := strings.TrimPrefix(name, "~")
		if strings.HasPrefix(pattern, "*") {
			pattern = "(?i)" + strings.TrimPrefix(pattern, "*")
		}
		parsed, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("existing nginx regex ownership cannot be proven disjoint")
		}
		return parsed.MatchString(host), nil
	}
	if strings.ContainsAny(name, `\$`) {
		return false, fmt.Errorf("existing nginx variable or escaped ownership cannot be proven disjoint")
	}
	name = strings.ToLower(name)
	host = strings.ToLower(host)
	switch {
	case strings.HasPrefix(name, "*."):
		return strings.HasSuffix(host, name[1:]), nil
	case strings.HasSuffix(name, ".*"):
		return strings.HasPrefix(host, strings.TrimSuffix(name, "*")), nil
	case strings.HasPrefix(name, "."):
		return host == name[1:] || strings.HasSuffix(host, name), nil
	default:
		return name == host, nil
	}
}
func nginxBrandConflicts(raw string, ownedPath string, domains []nginx.DomainConfig) error {
	directives := regexp.MustCompile(`(?s)\bserver_name\s+([^;]+);`)
	for _, section := range strings.Split(raw, "# configuration file ") {
		if newline := strings.IndexByte(section, '\n'); newline >= 0 {
			header := strings.TrimSuffix(strings.TrimSpace(section[:newline]), ":")
			if header == brandManagedNginxPath || header == ownedPath {
				continue
			}
		}
		for _, match := range directives.FindAllStringSubmatch(nginxWithoutComments(section), -1) {
			names := strings.Fields(match[1])
			for index := 0; index < len(names); index++ {
				name := names[index]
				if name == "~" || name == "~*" {
					if index+1 >= len(names) {
						return fmt.Errorf("invalid existing nginx regex")
					}
					index++
					name += names[index]
				}
				for _, domain := range domains {
					matches, err := nginxServerNameMatches(name, domain.FQDN)
					if err != nil {
						return err
					}
					if matches {
						return fmt.Errorf("%w: brand hostname is already owned by an existing nginx server", ErrRejected)
					}
				}
			}
		}
	}
	return nil
}

// hybridBrandFiles manages one additive include and never edits an operator's
// existing sites. Its caller journals these exact bytes with config.apply.
func (m *ApplyManager) hybridBrandFiles(domains []nginx.DomainConfig) (map[string]string, error) {
	path := m.target(brandManagedNginxPath)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && len(domains) == 0 {
		return map[string]string{}, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		uid, _ := fileOwner(info)
		if !info.Mode().IsRegular() || uid != os.Geteuid() || info.Size() > 2<<20 {
			return nil, fmt.Errorf("%w: unmanaged brand nginx file", ErrRejected)
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // One fixed managed path; regular ownership and size verified above.
		if readErr != nil || !strings.HasPrefix(string(content), brandManagedNginxMarker) {
			return nil, fmt.Errorf("%w: missing brand nginx ownership marker", ErrRejected)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	existing, err := m.brandNginxOutput(ctx)
	if err != nil {
		return nil, err
	}
	if err = nginxBrandConflicts(string(existing), path, domains); err != nil {
		return nil, err
	}
	generated, err := nginx.Generate(nginx.NodeConfig{PrimaryDomain: m.cfg.PrimaryDomain, StorageRoot: m.cfg.StorageRoot, BackendHTTP: "http://" + m.cfg.ListenAddr, WebmailBackend: fixedWebmailBackend(m.cfg)}, nginx.Snapshot{Domains: domains})
	if err != nil {
		return nil, err
	}
	return map[string]string{brandManagedNginxPath: brandManagedNginxMarker + generated[nginx.FileLocalConf]}, nil
}

// verifyHybridBrandInclude requires the real master configuration to load
// the exact owned include; syntax success alone cannot prove routing applied.
func (m *ApplyManager) verifyHybridBrandInclude(ctx context.Context) error {
	raw, err := m.brandNginxOutput(ctx)
	if err != nil {
		return err
	}
	wanted := []string{"# configuration file " + brandManagedNginxPath + ":", "# configuration file " + m.target(brandManagedNginxPath) + ":"}
	for _, line := range strings.Split(string(raw), "\n") {
		for _, name := range wanted {
			if strings.TrimSpace(line) == name {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: managed brand include is not loaded by nginx", ErrRejected)
}

// applyHybridBrandFiles is the short, serialized ACME bootstrap path used
// before network issuance. The ordinary config journal owns subsequent changes.
func (m *ApplyManager) applyHybridBrandFiles(domains []nginx.DomainConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, err := m.hybridBrandFiles(domains)
	if err != nil {
		return err
	}
	content, exists := files[brandManagedNginxPath]
	if !exists {
		return nil
	}
	path := m.target(brandManagedNginxPath)
	previous, err := saveFile(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = m.write(brandManagedNginxPath, []byte(content), 0644); err != nil {
		return err
	}
	reload := func() error {
		if e := m.run(ctx, "/usr/sbin/nginx", "-t"); e != nil {
			return e
		}
		if e := m.verifyHybridBrandInclude(ctx); e != nil {
			return e
		}
		return m.run(ctx, "/usr/bin/systemctl", "reload", "nginx")
	}
	if err = reload(); err == nil {
		return nil
	}
	var restoreErr error
	if previous.Exists {
		restoreErr = m.write(brandManagedNginxPath, previous.Content, os.FileMode(previous.Mode))
		if restoreErr == nil && os.Geteuid() == 0 {
			restoreErr = os.Chown(path, previous.UID, previous.GID)
		}
	} else {
		restoreErr = os.Remove(path)
	}
	if restoreErr == nil {
		// A first-install rollback removes the include; only the existing
		// operator configuration must validate and reload in that case.
		if e := m.run(ctx, "/usr/sbin/nginx", "-t"); e != nil {
			restoreErr = e
		} else {
			restoreErr = m.run(ctx, "/usr/bin/systemctl", "reload", "nginx")
		}
	}
	if restoreErr != nil {
		m.status.Degraded = true
		return fmt.Errorf("%w: hybrid brand apply failed and rollback needs review", ErrFailed)
	}
	return fmt.Errorf("%w: hybrid brand apply failed; previous file and service restored", ErrFailed)
}

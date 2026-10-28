package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const dnsSecondaryRuntime = "/etc/nsd/nsd.conf.d/urmail-secondary.runtime"
const dnsSecondaryUnit = "/etc/systemd/system/nsd.service.d/urmail-secondary.conf"

func readSecondaryBundle(path string) (DNSSecondaryBundle, error) {
	var bundle DNSSecondaryBundle
	info, err := os.Lstat(path)
	if err != nil {
		return bundle, err
	}
	uid, _ := fileOwner(info)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || uid != 0 || info.Size() > 8192 {
		return bundle, fmt.Errorf("%w: bundle requires root-owned regular 0600 file under 8192 bytes", ErrRejected)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // Fixed import/installed namespace; root-owned regular 0600 file and size are verified above.
	if err != nil {
		return bundle, err
	}
	if err = strictParams(raw, &bundle); err != nil {
		return bundle, err
	}
	return bundle, validateDNSSecondaryBundle(bundle)
}
func (m *ApplyManager) secondaryBundles() (map[string]DNSSecondaryBundle, error) {
	bundles := map[string]DNSSecondaryBundle{}
	directory := filepath.Join(m.dnsRoot(), "installed")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return bundles, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("invalid secondary state file")
		}
		bundle, err := readSecondaryBundle(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if entry.Name() != bundle.Domain+".json" {
			return nil, fmt.Errorf("secondary state identity mismatch")
		}
		bundles[bundle.Domain] = bundle
	}
	return bundles, nil
}
func renderSecondaryRuntime(addresses []string, bundles map[string]DNSSecondaryBundle) (string, error) {
	var out strings.Builder
	out.WriteString("# urMail DNS-only secondary. Private TSIG material.\nserver:\n  username: nsd\n  zonesdir: /var/lib/nsd/urmail-secondary\n  database: \"\"\n  xfrdfile: /var/lib/nsd/urmail-secondary/xfrd.state\n  pidfile: /run/nsd/nsd.pid\n")
	for _, address := range addresses {
		fmt.Fprintf(&out, "  ip-address: %s\n", address)
	}
	names := make([]string, 0, len(bundles))
	for name := range bundles {
		names = append(names, name)
	}
	sort.Strings(names)
	keys := map[string]string{}
	for _, name := range names {
		bundle := bundles[name]
		if err := validateDNSSecondaryBundle(bundle); err != nil {
			return "", err
		}
		if old, ok := keys[bundle.KeyName]; ok && old != bundle.Secret {
			return "", fmt.Errorf("conflicting TSIG key identity")
		}
		if _, ok := keys[bundle.KeyName]; !ok {
			fmt.Fprintf(&out, "\nkey:\n  name: %q\n  algorithm: hmac-sha256\n  secret: %q\n", bundle.KeyName, bundle.Secret)
			keys[bundle.KeyName] = bundle.Secret
		}
	}

	for _, name := range names {
		bundle := bundles[name]
		fmt.Fprintf(&out, "\nzone:\n  name: %q\n  zonefile: %q\n  outgoing-interface: %s\n  allow-notify: %s %s\n  request-xfr: AXFR %s %s\n", bundle.Domain, bundle.Domain+".zone", bundle.Address, bundle.PrimaryAddress, bundle.KeyName, bundle.PrimaryAddress, bundle.KeyName)
	}
	return out.String(), nil
}
func (m *ApplyManager) installSecondaryDNS(domain string) (string, string, error) {
	if err := protectedDNSDirectory(m.dnsRoot()); err != nil {
		return "", "", err
	}
	imports := filepath.Join(m.dnsRoot(), "imports")
	if err := protectedDNSDirectory(imports); err != nil {
		return "", "", err
	}
	bundle, err := readSecondaryBundle(filepath.Join(imports, domain+".json"))
	if err != nil {
		return "", "", err
	}
	if bundle.Domain != domain || bundle.Hostname != m.cfg.MyHostname {
		return "", "", fmt.Errorf("%w: secondary bundle is for another zone or node", ErrRejected)
	}
	matched := false
	for _, address := range m.cfg.DNSListenAddresses {
		if address == bundle.Address {
			matched = true
		}
	}
	if !matched {
		return "", "", fmt.Errorf("%w: secondary address does not belong to this node", ErrRejected)
	}
	bundles, err := m.secondaryBundles()
	if err != nil {
		return "", "", err
	}
	bundles[domain] = bundle
	content, err := renderSecondaryRuntime(m.cfg.DNSListenAddresses, bundles)
	if err != nil {
		return "", "", err
	}
	installed := filepath.Join(m.dnsRoot(), "installed")
	if err = protectedDNSDirectory(installed); err != nil {
		return "", "", err
	}
	statePath := filepath.Join(installed, domain+".json")
	paths := []string{m.target(dnsSecondaryRuntime), m.target(dnsSecondaryUnit), statePath}
	saved := map[string]savedFile{}
	for _, path := range paths {
		saved[path], err = saveFile(path)
		if err != nil {
			return "", "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	active := m.run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "nsd") == nil
	rollback := func() {
		for _, path := range paths {
			prior := saved[path]
			if prior.Exists {
				_, _ = writeFileAtomic(path, prior.Content, os.FileMode(prior.Mode), []string{filepath.Dir(path)})
			} else {
				_ = os.Remove(path)
			}
		}
		if saved[paths[0]].Exists {
			_ = chownGroup(paths[0], "nsd")
		}
		_ = m.run(context.Background(), "/usr/bin/systemctl", "daemon-reload")
		if active {
			_ = m.run(context.Background(), "/usr/bin/systemctl", "restart", "nsd")
		} else {
			_ = m.run(context.Background(), "/usr/bin/systemctl", "stop", "nsd")
		}
	}
	for _, directory := range []string{filepath.Dir(paths[0]), filepath.Dir(paths[1]), m.target("/var/lib/nsd/urmail-secondary")} {
		if err = os.MkdirAll(directory, 0750); err != nil {
			return "", "", err
		}
	}
	if err = chownGroup(m.target("/var/lib/nsd/urmail-secondary"), "nsd"); err != nil {
		return "", "", err
	}
	// Transfer files are written by the unprivileged NSD worker.
	if groupErr := chownNSDDirectory(m.target("/var/lib/nsd/urmail-secondary")); groupErr != nil {
		return "", "", groupErr
	}
	if _, err = writeFileAtomic(paths[0], []byte(content), 0640, []string{filepath.Dir(paths[0])}); err != nil {
		rollback()
		return "", "", err
	}
	if err = chownGroup(paths[0], "nsd"); err != nil {
		rollback()
		return "", "", err
	}
	if _, err = m.dnsOutput(ctx, "/usr/sbin/nsd-checkconf", paths[0]); err != nil {
		rollback()
		return "", "", err
	}
	unit := "[Service]\nType=simple\nExecStart=\nExecStart=/usr/sbin/nsd -d -c " + dnsSecondaryRuntime + "\n"
	if _, err = writeFileAtomic(paths[1], []byte(unit), 0644, []string{filepath.Dir(paths[1])}); err != nil {
		rollback()
		return "", "", err
	}
	if err = m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); err == nil {
		err = m.run(ctx, "/usr/bin/systemctl", "restart", "nsd")
	}
	if err != nil {
		rollback()
		return "", "", err
	}
	data, err := json.Marshal(bundle) //nolint:gosec // Root-only installed state must retain TSIG for NSD restarts; output is only a protected 0600 file.
	if err == nil {
		_, err = writeFileAtomic(statePath, data, 0600, []string{installed})
	}
	if err != nil {
		rollback()
		return "", "", err
	}
	// Installation is separate from replication readiness. Status must observe SOA.
	return `{"installed":true,"replication_verified":false}`, "", nil
}
func (m *ApplyManager) secondaryDNSStatus(domain string) (string, string, error) {
	bundles, err := m.secondaryBundles()
	if err != nil {
		return "", "", err
	}
	bundle, ok := bundles[domain]
	if !ok {
		return "", "", fmt.Errorf("%w: secondary zone is not installed", ErrRejected)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	secondary := m.dnsSOA(ctx, bundle.Address, domain)
	primary := m.dnsSOA(ctx, bundle.PrimaryAddress, domain)
	data, err := json.Marshal(map[string]any{"domain": domain, "installed": true, "answering": secondary != "", "soa": secondary, "primary_soa": primary, "same_soa": primary != "" && primary == secondary, "independent_failure_domain_verified": false})
	return string(data), "", err
}

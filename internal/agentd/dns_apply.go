package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
)

func (m *ApplyManager) enrichDNSSnapshot(snapshot *nsd.Snapshot) error {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	if m.cfg != nil && m.cfg.PreserveExternalNSD {
		for _, zone := range snapshot.Zones {
			if zone.Sign || zone.Secondary != nil {
				return fmt.Errorf("%w: preserved external NSD cannot be managed", ErrRejected)
			}
		}
		return nil
	}
	for index := range snapshot.Zones {
		zone := &snapshot.Zones[index]
		if !zone.Sign {
			continue
		}
		state, err := m.loadDNSState(zone.Domain)
		if err != nil {
			return err
		}
		signingEpoch := state.SigningEpoch
		if m.cfg != nil && m.cfg.BackgroundWorkersAllowed() && !state.PublishedAt.IsZero() && signatureExpiry(m.target("/etc/nsd/zones/"+zone.Domain+".signed")).Before(time.Now().Add(5*24*time.Hour)) {
			signingEpoch = time.Now().UTC()
			// In-memory calculation only; do not overwrite disk live state.json before commit.
		}
		previous := state.Previous
		if state.Phase == "retired" {
			previous = nil
		}
		raw, err := json.Marshal(struct {
			Current      dnsKeyGeneration
			Previous     *dnsKeyGeneration
			SigningEpoch time.Time
		}{state.Current, previous, signingEpoch})
		if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		zone.SigningVersion = hex.EncodeToString(hash[:])
	}
	return nil
}
func (m *ApplyManager) dnsTSIGContent(snapshot nsd.Snapshot) (string, error) {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	keys := map[string]string{}
	for _, zone := range snapshot.Zones {
		if zone.Secondary == nil {
			continue
		}
		state, err := m.loadDNSState(zone.Domain)
		if err != nil {
			return "", err
		}
		bundle := DNSSecondaryBundle{Version: 1, Domain: zone.Domain, Hostname: zone.Secondary.Hostname, Address: zone.Secondary.Address, PrimaryAddress: zone.Secondary.PrimaryAddress, KeyName: state.TSIGName, Secret: state.TSIGSecret}
		if err = validateDNSSecondaryBundle(bundle); err != nil {
			return "", err
		}
		if bundle.KeyName != zone.Secondary.TSIGKeyName {
			return "", fmt.Errorf("secondary TSIG intent is not provisioned")
		}
		if prior, ok := keys[bundle.KeyName]; ok && prior != bundle.Secret {
			return "", fmt.Errorf("conflicting TSIG key name")
		}
		keys[bundle.KeyName] = bundle.Secret
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	var content strings.Builder
	for _, name := range names {
		fmt.Fprintf(&content, "key:\n  name: %q\n  algorithm: hmac-sha256\n  secret: %q\n\n", name, keys[name])
	}
	return content.String(), nil
}
func (m *ApplyManager) addDNSSecureFiles(files map[string]string, snapshot nsd.Snapshot) error {
	content, err := m.dnsTSIGContent(snapshot)
	if err != nil {
		return err
	}
	if content != "" {
		hash := sha256.Sum256([]byte(content))
		files[dnsTSIGPath] = "# root-owned TSIG generation " + hex.EncodeToString(hash[:]) + "\n"
	}
	return nil
}
func (m *ApplyManager) installDNSDerived(ctx context.Context, snapshot nsd.Snapshot) error {
	if m.cfg != nil && (m.cfg.PreserveExternalNSD || m.cfg.WorkersHeld) {
		return nil
	}
	content, err := m.dnsTSIGContent(snapshot)
	if err != nil {
		return err
	}
	if content != "" {
		if err = m.write(dnsTSIGPath, []byte(content), 0640); err != nil {
			return err
		}
		if err = chownGroup(m.target(dnsTSIGPath), "nsd"); err != nil {
			return err
		}
	}
	for _, zone := range snapshot.Zones {
		if zone.Sign {
			if err = m.signDNSZone(ctx, zone.Domain); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *ApplyManager) dnsDerivedCurrent(snapshot nsd.Snapshot) bool {
	if m.cfg != nil && m.cfg.PreserveExternalNSD {
		return true
	}
	for _, zone := range snapshot.Zones {
		if zone.Sign && signatureExpiry(m.target("/etc/nsd/zones/"+zone.Domain+".signed")).Before(time.Now().Add(5*24*time.Hour)) {
			return false
		}
	}
	content, err := m.dnsTSIGContent(snapshot)
	if err != nil {
		return false
	}
	if content != "" {
		raw, readErr := os.ReadFile(m.target(dnsTSIGPath))
		if readErr != nil || string(raw) != content {
			return false
		}
	}
	return true
}

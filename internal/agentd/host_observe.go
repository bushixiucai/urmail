package agentd

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OpHostObserve reports fixed host resources and daemon state without secrets.
const OpHostObserve = "host.observe"

// HostObservation contains counters, filesystem capacity and fixed daemon names.
type HostObservation struct {
	ObservedAt             time.Time                        `json:"observed_at"`
	CPUTotal               uint64                           `json:"cpu_total"`
	CPUIdle                uint64                           `json:"cpu_idle"`
	MemoryTotal            uint64                           `json:"memory_total"`
	MemoryAvailable        uint64                           `json:"memory_available"`
	Filesystems            map[string]FilesystemObservation `json:"filesystems"`
	Daemons                map[string]bool                  `json:"daemons"`
	CertificateExpiresAt   *time.Time                       `json:"certificate_expires_at"`
	CertificateUnavailable bool                             `json:"certificate_unavailable"`
}

// FilesystemObservation exposes only bounded fixed storage roots.
type FilesystemObservation struct {
	Total           uint64 `json:"total"`
	Available       uint64 `json:"available"`
	Inodes          uint64 `json:"inodes"`
	AvailableInodes uint64 `json:"available_inodes"`
}

// ObserveHost accepts no paths, shell arguments, units or certificate names.
func (m *ApplyManager) ObserveHost(raw json.RawMessage) (string, string, error) {
	var empty struct{}
	if err := strictParams(raw, &empty); err != nil {
		return "", "", err
	}
	cpu, err := os.ReadFile("/proc/stat")
	if err != nil {
		return "", "", errors.New("host CPU source unavailable")
	}
	memory, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", "", errors.New("host memory source unavailable")
	}
	observation, err := parseHostCounters(string(cpu), string(memory))
	if err != nil {
		return "", "", err
	}
	observation.ObservedAt = time.Now().UTC()
	observation.Filesystems = map[string]FilesystemObservation{}
	observation.Daemons = map[string]bool{}
	for _, path := range []string{"/var/mail", "/var/lib/urmail"} {
		var stat syscall.Statfs_t
		if err = syscall.Statfs(path, &stat); err != nil {
			return "", "", fmt.Errorf("host fixed storage unavailable")
		}
		if stat.Bsize <= 0 {
			return "", "", errors.New("host fixed storage block size unavailable")
		}
		blockSize := uint64(stat.Bsize)
		observation.Filesystems[path] = FilesystemObservation{Total: stat.Blocks * blockSize, Available: stat.Bavail * blockSize, Inodes: stat.Files, AvailableInodes: stat.Ffree}
	}
	for _, unit := range []string{"postfix", "dovecot", "nginx", "opendkim", "nsd"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		observation.Daemons[unit] = exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit+".service").Run() == nil
		cancel()
	} // #nosec G204 G702 -- executable and service allowlist are fixed; no shell.
	certificate, readErr := os.ReadFile(certificateCertPath(m.certificatePaths(m.hostname()))) // #nosec G304 -- fixed root-owned certificate path from validated hostname.
	observation.CertificateUnavailable = true
	if readErr == nil {
		if block, _ := pem.Decode(certificate); block != nil {
			if leaf, e := x509.ParseCertificate(block.Bytes); e == nil {
				observation.CertificateExpiresAt = &leaf.NotAfter
				observation.CertificateUnavailable = false
			}
		}
	}
	encoded, err := json.Marshal(observation)
	return string(encoded), "", err
}

func parseHostCounters(cpu, memory string) (HostObservation, error) {
	var result HostObservation
	lines := strings.SplitN(cpu, "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return result, errors.New("invalid host CPU counters")
	}
	for index, value := range fields[1:] {
		if index >= 8 {
			break
		}
		counter, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return result, errors.New("invalid host CPU counter")
		}
		result.CPUTotal += counter
		if index == 3 || index == 4 {
			result.CPUIdle += counter
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(memory))
	for scanner.Scan() {
		values := strings.Fields(scanner.Text())
		if len(values) < 2 {
			continue
		}
		value, err := strconv.ParseUint(values[1], 10, 64)
		if err != nil {
			continue
		}
		switch values[0] {
		case "MemTotal:":
			result.MemoryTotal = value * 1024
		case "MemAvailable:":
			result.MemoryAvailable = value * 1024
		}
	}
	if result.MemoryTotal == 0 || result.MemoryAvailable > result.MemoryTotal {
		return result, errors.New("invalid host memory counters")
	}
	return result, nil
}

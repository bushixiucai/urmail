package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

const maxSystemdUnitSourceBytes = 1 << 20

type systemdUnitConfig struct {
	Paths        []string
	Conditions   []string
	ExecStartPre []string
}

type systemdSource struct {
	Content     []byte
	Fingerprint string
}

type postfixGuardOps struct {
	list     func(context.Context) ([]string, error)
	observe  func(context.Context, string) (offlineWriterObservation, error)
	cat      func(context.Context, string) ([]byte, error)
	readFile func(string) (systemdSource, error)
}

type postfixTemplateEvidence struct {
	FragmentPath        string
	FragmentFingerprint string
	GuardPath           string
	GuardFingerprint    string
	Fingerprint         string
}

func livePostfixGuardOps() postfixGuardOps {
	return postfixGuardOps{
		list: func(ctx context.Context) ([]string, error) {
			out, err := runSystemctl(ctx, "list-units", "--all", "--type=service", "--no-legend", "--plain", "postfix@*.service")
			if err != nil {
				return nil, fmt.Errorf("list Postfix instances: %w", err)
			}
			seen := map[string]bool{}
			var units []string
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 || fields[0] == "No" {
					continue
				}
				unit := fields[0]
				if !validPostfixInstanceUnit(unit) || seen[unit] {
					return nil, fmt.Errorf("invalid or duplicate Postfix instance from systemd: %q", unit)
				}
				seen[unit] = true
				units = append(units, unit)
			}
			sort.Strings(units)
			return units, nil
		},
		observe: readOfflineWriterObservation,
		cat: func(ctx context.Context, unit string) ([]byte, error) {
			return runSystemctl(ctx, "cat", unit)
		},
		readFile: readSystemdConfigFile,
	}
}

func validPostfixInstanceUnit(unit string) bool {
	if !strings.HasPrefix(unit, "postfix@") || !strings.HasSuffix(unit, ".service") || unit == "postfix@.service" || strings.ContainsAny(unit, "/\\\x00\r\n\t ") {
		return false
	}
	instance := strings.TrimSuffix(strings.TrimPrefix(unit, "postfix@"), ".service")
	return instance != "" && !strings.Contains(instance, "..")
}

func trustedSystemdSourcePath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for _, root := range []string{
		"/etc/systemd/system", "/run/systemd/system", "/usr/lib/systemd/system",
		"/lib/systemd/system", "/usr/local/lib/systemd/system",
	} {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func trustedPostfixTemplateFragment(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "postfix@.service" {
		return false
	}
	for _, root := range []string{"/usr/lib/systemd/system", "/lib/systemd/system", "/usr/local/lib/systemd/system"} {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func readSystemdConfigFile(path string) (systemdSource, error) {
	if !trustedSystemdSourcePath(path) {
		return systemdSource{}, errors.New("systemd source path is outside trusted unit paths")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return systemdSource{}, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0644 || before.Size() <= 0 || before.Size() > maxSystemdUnitSourceBytes {
		return systemdSource{}, fmt.Errorf("systemd source %s has unsafe mode/type/size", filepath.Base(path))
	}
	owner, group := fileOwner(before)
	if owner != 0 || group != 0 {
		return systemdSource{}, fmt.Errorf("systemd source %s is not root-owned", filepath.Base(path))
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return systemdSource{}, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return systemdSource{}, err
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	if !beforeOK || !openedOK || beforeStat.Dev != openedStat.Dev || beforeStat.Ino != openedStat.Ino || before.Size() != opened.Size() {
		return systemdSource{}, errors.New("systemd source changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSystemdUnitSourceBytes+1))
	if err != nil {
		return systemdSource{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return systemdSource{}, err
	}
	afterStat, afterOK := after.Sys().(*syscall.Stat_t)
	if !afterOK || len(raw) > maxSystemdUnitSourceBytes || int64(len(raw)) != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || afterStat.Dev != openedStat.Dev || afterStat.Ino != openedStat.Ino {
		return systemdSource{}, errors.New("systemd source changed during bounded read")
	}
	fingerprint := fmt.Sprintf("%s:%d:%d:%d:%d:%d", hashBytesLocal(raw), opened.Mode(), owner, group, openedStat.Dev, openedStat.Ino)
	fingerprint += fmt.Sprintf(":%d:%d", opened.Size(), opened.ModTime().UnixNano())
	return systemdSource{Content: raw, Fingerprint: fingerprint}, nil
}

func parseSystemdUnitConfig(raw []byte) (systemdUnitConfig, error) {
	if len(raw) == 0 || len(raw) > 16*maxSystemdUnitSourceBytes {
		return systemdUnitConfig{}, errors.New("systemd cat output empty or oversized")
	}
	var cfg systemdUnitConfig
	section := ""
	seenPaths := map[string]bool{}
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if strings.HasPrefix(line, "# ") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "# "))
			if filepath.IsAbs(candidate) {
				if filepath.Clean(candidate) != candidate || seenPaths[candidate] {
					return systemdUnitConfig{}, errors.New("systemd cat has unsafe or duplicate source path")
				}
				seenPaths[candidate] = true
				cfg.Paths = append(cfg.Paths, candidate)
				section = ""
				continue
			}
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case section == "Unit" && key == "ConditionPathExists":
			if value == "" {
				cfg.Conditions = nil
			} else {
				cfg.Conditions = append(cfg.Conditions, value)
			}
		case section == "Service" && key == "ExecStartPre":
			if value == "" {
				cfg.ExecStartPre = nil
			} else {
				cfg.ExecStartPre = append(cfg.ExecStartPre, value)
			}
		}
	}
	return cfg, nil
}

func verifySystemdHoldGuardConfig(cfg systemdUnitConfig, holdPath string, requireExecStartPre bool) error {
	if !containsString(cfg.Conditions, "!"+holdPath) {
		return errors.New("effective systemd ConditionPathExists hold guard is absent")
	}
	if requireExecStartPre && !containsString(cfg.ExecStartPre, "/usr/bin/test ! -f "+holdPath) {
		return errors.New("effective systemd ExecStartPre hold guard is absent")
	}
	return nil
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func verifySystemdExecStartPreProperty(value, holdPath string) bool {
	return strings.Contains(value, "path=/usr/bin/test ; argv[]=/usr/bin/test ! -f "+holdPath+" ;")
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, item := range a {
		set[item]++
	}
	for _, item := range b {
		set[item]--
	}
	for _, count := range set {
		if count != 0 {
			return false
		}
	}
	return true
}

// systemd.unit(5) specifies that instantiated units merge instance .d files
// before template .d files, and a same-named instance drop-in takes precedence.
// Therefore the template cat proves protection for new instances without an
// instance override, while each currently loaded instance must separately prove
// its merged guard. `systemctl show` rejects the template unit name itself.
func readPostfixTemplate(ctx context.Context, holdPath string, ops postfixGuardOps) (postfixTemplateEvidence, error) {
	cat, err := ops.cat(ctx, "postfix@.service")
	if err != nil {
		return postfixTemplateEvidence{}, fmt.Errorf("systemctl cat Postfix template: %w", err)
	}
	cfg, err := parseSystemdUnitConfig(cat)
	if err != nil {
		return postfixTemplateEvidence{}, err
	}
	var fragments []string
	for _, path := range cfg.Paths {
		if filepath.Base(path) == "postfix@.service" {
			fragments = append(fragments, path)
		}
	}
	if len(fragments) != 1 || !trustedPostfixTemplateFragment(fragments[0]) {
		return postfixTemplateEvidence{}, errors.New("systemctl cat did not identify exactly one trusted Postfix template fragment")
	}
	fragment := fragments[0]
	guard := filepath.Join(maintenance.DefaultDropInDir, filepath.Base(fragment)+".d", maintenance.DropInFileName)
	if !containsString(cfg.Paths, guard) {
		return postfixTemplateEvidence{}, errors.New("systemctl cat does not include the template hold drop-in")
	}
	files := make(map[string]systemdSource, len(cfg.Paths))
	for _, path := range cfg.Paths {
		file, err := ops.readFile(path)
		if err != nil {
			return postfixTemplateEvidence{}, fmt.Errorf("read template source %s: %w", filepath.Base(path), err)
		}
		files[path] = file
	}
	guardCfg, err := parseSystemdUnitConfig(files[guard].Content)
	if err != nil {
		return postfixTemplateEvidence{}, err
	}
	if err := verifySystemdHoldGuardConfig(guardCfg, holdPath, true); err != nil {
		return postfixTemplateEvidence{}, fmt.Errorf("template guard file content: %w", err)
	}
	if err := verifySystemdHoldGuardConfig(cfg, holdPath, true); err != nil {
		return postfixTemplateEvidence{}, fmt.Errorf("Postfix template guard: %w", err)
	}
	var fingerprint strings.Builder
	fingerprint.WriteString(hashBytesLocal(cat))
	for _, path := range cfg.Paths {
		fingerprint.WriteString(path)
		fingerprint.WriteByte('=')
		fingerprint.WriteString(files[path].Fingerprint)
		fingerprint.WriteByte('\n')
	}
	return postfixTemplateEvidence{FragmentPath: fragment, FragmentFingerprint: files[fragment].Fingerprint, GuardPath: guard, GuardFingerprint: files[guard].Fingerprint, Fingerprint: hashBytesLocal([]byte(fingerprint.String()))}, nil
}

func readPostfixInstance(ctx context.Context, unit, holdPath string, template postfixTemplateEvidence, ops postfixGuardOps) (offlineWriterObservation, string, error) {
	if !validPostfixInstanceUnit(unit) {
		return offlineWriterObservation{}, "", fmt.Errorf("invalid Postfix instance name %q", unit)
	}
	observation, err := ops.observe(ctx, unit)
	if err != nil {
		return offlineWriterObservation{}, "", fmt.Errorf("inspect Postfix instance %s: %w", unit, err)
	}
	if observation.Unit != unit || observation.Load != "loaded" || observation.Active != "inactive" || observation.MainPID != "0" || observation.ControlPID != "0" || observation.ControlGroup != "" {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s is not proven inactive and quiescent", unit)
	}
	if observation.FragmentPath != template.FragmentPath || observation.NeedDaemonReload != "no" {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s fragment is unexpected or daemon reload is pending", unit)
	}
	if !verifySystemdExecStartPreProperty(observation.ExecStartPre, holdPath) {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s effective ExecStartPre hold guard is absent", unit)
	}
	cat, err := ops.cat(ctx, unit)
	if err != nil {
		return offlineWriterObservation{}, "", fmt.Errorf("systemctl cat Postfix instance %s: %w", unit, err)
	}
	cfg, err := parseSystemdUnitConfig(cat)
	if err != nil {
		return offlineWriterObservation{}, "", err
	}
	if len(cfg.Paths) == 0 || cfg.Paths[0] != observation.FragmentPath {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s cat sources disagree with FragmentPath", unit)
	}
	dropins := strings.Fields(observation.DropInPaths)
	if !sameStringSet(dropins, cfg.Paths[1:]) {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s DropInPaths disagree with systemctl cat", unit)
	}
	instanceGuard := filepath.Join(maintenance.DefaultDropInDir, unit+".d", maintenance.DropInFileName)
	matchingGuards := 0
	selectedGuard := ""
	for _, path := range dropins {
		if filepath.Base(path) == maintenance.DropInFileName {
			matchingGuards++
			selectedGuard = path
		}
	}
	if matchingGuards != 1 || (selectedGuard != instanceGuard && selectedGuard != template.GuardPath) {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s has no unique effective template/instance guard", unit)
	}
	instanceSource, instanceErr := ops.readFile(instanceGuard)
	instanceExists := instanceErr == nil
	if instanceErr != nil && !errors.Is(instanceErr, os.ErrNotExist) {
		return offlineWriterObservation{}, "", fmt.Errorf("inspect Postfix instance override %s: %w", unit, instanceErr)
	}
	if instanceExists {
		instanceCfg, err := parseSystemdUnitConfig(instanceSource.Content)
		if err != nil {
			return offlineWriterObservation{}, "", err
		}
		if err := verifySystemdHoldGuardConfig(instanceCfg, holdPath, true); err != nil {
			return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance override %s: %w", unit, err)
		}
		if selectedGuard != instanceGuard {
			return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s override exists but is not effective", unit)
		}
	} else if selectedGuard != template.GuardPath {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s reports a missing instance guard as effective", unit)
	}
	var fingerprint strings.Builder
	fingerprint.WriteString(fmt.Sprintf("%+v\n", observation))
	fingerprint.WriteString(hashBytesLocal(cat))
	for _, path := range cfg.Paths {
		file, err := ops.readFile(path)
		if err != nil {
			return offlineWriterObservation{}, "", fmt.Errorf("read Postfix instance source %s: %w", filepath.Base(path), err)
		}
		if path == template.FragmentPath && file.Fingerprint != template.FragmentFingerprint {
			return offlineWriterObservation{}, "", errors.New("Postfix template fragment changed during instance verification")
		}
		if path == template.GuardPath && file.Fingerprint != template.GuardFingerprint {
			return offlineWriterObservation{}, "", errors.New("Postfix template guard changed during instance verification")
		}
		if path == instanceGuard && instanceExists && file.Fingerprint != instanceSource.Fingerprint {
			return offlineWriterObservation{}, "", errors.New("Postfix instance override changed during instance verification")
		}
		fingerprint.WriteString(path)
		fingerprint.WriteByte('=')
		fingerprint.WriteString(file.Fingerprint)
		fingerprint.WriteByte('\n')
	}
	if err := verifySystemdHoldGuardConfig(cfg, holdPath, true); err != nil {
		return offlineWriterObservation{}, "", fmt.Errorf("Postfix instance %s effective hold guard: %w", unit, err)
	}
	return observation, hashBytesLocal([]byte(fingerprint.String())), nil
}

func verifyPostfixTemplateAndInstances(ctx context.Context, holdPath string) (string, error) {
	return verifyPostfixTemplateAndInstancesWithOps(ctx, holdPath, livePostfixGuardOps())
}

func verifyPostfixTemplateFenceDropIn(unit, holdPath string) (string, error) {
	if unit != "postfix@.service" {
		return "", errors.New("unexpected Postfix template unit")
	}
	return verifyPostfixTemplateAndInstances(context.Background(), holdPath)
}

func verifyPostfixTemplateAndInstancesWithOps(ctx context.Context, holdPath string, ops postfixGuardOps) (string, error) {
	template, err := readPostfixTemplate(ctx, holdPath, ops)
	if err != nil {
		return "", err
	}
	units, err := ops.list(ctx)
	if err != nil {
		return "", fmt.Errorf("enumerate Postfix instances: %w", err)
	}
	units = append([]string(nil), units...)
	sort.Strings(units)
	for i, unit := range units {
		if !validPostfixInstanceUnit(unit) || (i > 0 && units[i-1] == unit) {
			return "", fmt.Errorf("invalid or duplicate Postfix instance %q", unit)
		}
	}
	observations := make(map[string]offlineWriterObservation, len(units))
	fingerprints := make(map[string]string, len(units))
	var combined strings.Builder
	combined.WriteString(template.Fingerprint)
	for _, unit := range units {
		observation, fingerprint, err := readPostfixInstance(ctx, unit, holdPath, template, ops)
		if err != nil {
			return "", err
		}
		observations[unit] = observation
		fingerprints[unit] = fingerprint
		combined.WriteString("\n")
		combined.WriteString(unit)
		combined.WriteByte('=')
		combined.WriteString(fingerprint)
	}
	unitsAfter, err := ops.list(ctx)
	if err != nil {
		return "", fmt.Errorf("re-enumerate Postfix instances: %w", err)
	}
	unitsAfter = append([]string(nil), unitsAfter...)
	sort.Strings(unitsAfter)
	if !sameStringSet(units, unitsAfter) {
		return "", errors.New("Postfix instance set changed during guard verification")
	}
	templateAfter, err := readPostfixTemplate(ctx, holdPath, ops)
	if err != nil {
		return "", err
	}
	if templateAfter.Fingerprint != template.Fingerprint {
		return "", errors.New("Postfix template or template guard changed during verification")
	}
	for _, unit := range units {
		observation, fingerprint, err := readPostfixInstance(ctx, unit, holdPath, templateAfter, ops)
		if err != nil {
			return "", err
		}
		if observation != observations[unit] || fingerprint != fingerprints[unit] {
			return "", fmt.Errorf("Postfix instance %s or its effective guard changed during verification", unit)
		}
	}
	return hashBytesLocal([]byte(combined.String())), nil
}

func verifyPostfixGuardConfigForTest(raw []byte, holdPath string) error {
	cfg, err := parseSystemdUnitConfig(raw)
	if err != nil {
		return err
	}
	return verifySystemdHoldGuardConfig(cfg, holdPath, true)
}

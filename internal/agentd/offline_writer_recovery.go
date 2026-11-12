package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

type offlineWriterObservation struct {
	Unit, Load, Active, Result, MainPID, ControlPID, ControlGroup                                 string
	InvocationID, ExecMainStatus, ExecMainStartTimestamp, DropInPaths, FragmentPath, ExecStartPre string
	NeedDaemonReload                                                                              string
}

var recoverableFailedWriters = map[string]string{
	"urmail-cert-renew.service": "urmail-cert-renew.timer",
	"urmail-usage-sync.service": "urmail-usage-sync.timer",
}

// assessQuiescentFailedWriter is the explicit exception for offline pending
// recovery only. It never changes systemd state and never treats failed as
// inactive for the normal paired writer gate.
func assessQuiescentFailedWriter(o, timer offlineWriterObservation, pendingJobs []string, guardOK bool) error {
	if _, ok := recoverableFailedWriters[o.Unit]; !ok {
		return fmt.Errorf("failed writer %s is not in offline recovery allowlist", o.Unit)
	}
	if o.Load != "loaded" || o.Active != "failed" || o.Result != "exit-code" || o.InvocationID == "" || o.ExecMainStartTimestamp == "" || o.ExecMainStatus == "" {
		return fmt.Errorf("failed writer %s state/result/invocation evidence incomplete", o.Unit)
	}
	if o.MainPID != "0" || o.ControlPID != "0" || o.ControlGroup != "" {
		return fmt.Errorf("failed writer %s has PID/control/cgroup assignment", o.Unit)
	}
	if timer.Load != "loaded" || timer.Active != "inactive" {
		return fmt.Errorf("timer for failed writer %s is not loaded/inactive", o.Unit)
	}
	for _, job := range pendingJobs {
		for _, field := range strings.Fields(job) {
			if field == o.Unit || field == recoverableFailedWriters[o.Unit] {
				return errors.New("failed writer or timer has a queued systemd job")
			}
		}
	}
	if !guardOK {
		return fmt.Errorf("failed writer %s or its timer lacks the active hold guard", o.Unit)
	}
	return nil
}

// verifyOfflineWritersForRecovery proves all ordinary writers inactive. Only
// cert-renew and usage-sync may remain in failed state, and then only with zero
// Main/Control PID, no cgroup tasks/jobs, inactive guarded timer, effective
// hold guards, and unchanged hold bytes. Their failed state is archived in a
// private directory and left untouched (no reset-failed/start).
func verifyOfflineWritersForRecovery(ctx context.Context, maintenanceRoot, holdHash, stateRoot string, archive bool) error {
	jobsRaw, err := runSystemctl(ctx, "list-jobs", "--no-legend")
	if err != nil {
		return fmt.Errorf("inspect systemd jobs: %w", err)
	}
	jobs := strings.Split(string(jobsRaw), "\n")
	if len(nonemptySystemdJobs(jobsRaw)) != 0 {
		return errors.New("systemd jobs are pending before offline recovery")
	}
	if _, err := verifyPostfixTemplateAndInstances(ctx, filepath.Join(maintenanceRoot, maintenance.DefaultUpgradeHoldFile)); err != nil {
		return fmt.Errorf("verify Postfix template and instances: %w", err)
	}
	var evidenceDir string
	if archive {
		evidenceDir, err = createOfflineWriterEvidenceDir(stateRoot)
		if err != nil {
			return err
		}
		if err = writePrivateEvidence(filepath.Join(evidenceDir, "jobs.txt"), jobsRaw); err != nil {
			return err
		}
	}
	failedBefore := map[string]offlineWriterObservation{}
	timersBefore := map[string]offlineWriterObservation{}
	for _, unit := range maintenance.DefaultWriterUnits {
		o, err := readOfflineWriterObservation(ctx, unit)
		if err != nil {
			return err
		}
		if o.Load == "not-found" && o.Active == "inactive" {
			continue
		}
		if o.Load != "loaded" {
			return fmt.Errorf("writer %s has unknown load state %q", unit, o.Load)
		}
		if o.Active == "inactive" {
			continue
		}
		if o.Active != "failed" {
			return fmt.Errorf("writer %s is not proven inactive (load=%s active=%s)", unit, o.Load, o.Active)
		}
		timerUnit, allowed := recoverableFailedWriters[unit]
		if !allowed {
			return fmt.Errorf("writer %s failed and is not in offline recovery allowlist", unit)
		}
		timer, err := readOfflineWriterObservation(ctx, timerUnit)
		if err != nil {
			return err
		}
		guardOK, err := verifyEffectiveHoldGuards(unit, o.DropInPaths, timerUnit, timer.DropInPaths, maintenanceRoot)
		if err != nil {
			return err
		}
		if err := assessQuiescentFailedWriter(o, timer, jobs, guardOK); err != nil {
			return err
		}
		failedBefore[unit] = o
		timersBefore[unit] = timer
		if archive {
			if err := archiveFailedWriter(ctx, evidenceDir, o, timer, 0, jobs); err != nil {
				return err
			}
		}
	}
	jobsAfter, err := runSystemctl(ctx, "list-jobs", "--no-legend")
	if err != nil {
		return fmt.Errorf("recheck systemd jobs: %w", err)
	}
	if len(nonemptySystemdJobs(jobsAfter)) != 0 {
		return errors.New("systemd job appeared during offline recovery check")
	}
	for unit, before := range failedBefore {
		timerUnit := recoverableFailedWriters[unit]
		for _, job := range strings.Split(string(jobsAfter), "\n") {
			for _, field := range strings.Fields(job) {
				if field == unit || field == timerUnit {
					return errors.New("failed writer or timer job appeared during recovery check")
				}
			}
		}
		after, err := readOfflineWriterObservation(ctx, unit)
		if err != nil {
			return err
		}
		timerAfter, err := readOfflineWriterObservation(ctx, timerUnit)
		if err != nil {
			return err
		}
		if err := verifyFailedWriterStable(before, after, timersBefore[unit], timerAfter); err != nil {
			return fmt.Errorf("writer %s: %w", unit, err)
		}
		if _, err := verifyEffectiveHoldGuards(unit, after.DropInPaths, timerUnit, timerAfter.DropInPaths, maintenanceRoot); err != nil {
			return fmt.Errorf("recheck hold guards for writer %s: %w", unit, err)
		}
	}

	holdNow, err := verifyOfflineFence(maintenanceRoot, maintenance.DefaultDropInDir)
	if err != nil {
		return fmt.Errorf("recheck active hold after writer sampling: %w", err)
	}
	if holdNow != holdHash {
		return errors.New("upgrade hold or Postfix template guard changed during writer-state sampling")
	}
	return nil
}

func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/root"}
	return cmd.Output()
}

func nonemptySystemdJobs(raw []byte) []string {
	var jobs []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "No jobs running." {
			continue
		}
		jobs = append(jobs, line)
	}
	return jobs
}

func verifyFailedWriterStable(before, after, timerBefore, timerAfter offlineWriterObservation) error {
	if after.Unit != before.Unit || after.Load != before.Load || after.Active != "failed" || after.Result != before.Result || after.InvocationID != before.InvocationID || after.ExecMainStatus != before.ExecMainStatus || after.ExecMainStartTimestamp != before.ExecMainStartTimestamp || after.MainPID != "0" || after.ControlPID != "0" || after.ControlGroup != "" {
		return errors.New("failed writer state changed or is not quiescent")
	}
	if timerAfter.Unit != timerBefore.Unit || timerAfter.Load != "loaded" || timerAfter.Active != "inactive" || timerAfter.InvocationID != timerBefore.InvocationID || timerAfter.MainPID != "0" || timerAfter.ControlPID != "0" || timerAfter.ControlGroup != "" {
		return errors.New("timer state changed or is not inactive")
	}
	return nil
}

func readOfflineWriterObservation(ctx context.Context, unit string) (offlineWriterObservation, error) {
	props := []string{"LoadState", "ActiveState", "Result", "MainPID", "ControlPID", "ControlGroup", "InvocationID", "ExecMainStatus", "ExecMainStartTimestamp", "DropInPaths", "FragmentPath", "ExecStartPre", "NeedDaemonReload"}
	args := []string{"show"}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	args = append(args, unit)
	out, err := runSystemctl(ctx, args...)
	if err != nil {
		return offlineWriterObservation{}, fmt.Errorf("inspect writer %s: %w", unit, err)
	}
	o := offlineWriterObservation{Unit: unit}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			o.Load = v
		case "ActiveState":
			o.Active = v
		case "Result":
			o.Result = v
		case "MainPID":
			o.MainPID = v
		case "ControlPID":
			o.ControlPID = v
		case "ControlGroup":
			o.ControlGroup = v
		case "InvocationID":
			o.InvocationID = v
		case "ExecMainStatus":
			o.ExecMainStatus = v
		case "ExecMainStartTimestamp":
			o.ExecMainStartTimestamp = v
		case "DropInPaths":
			o.DropInPaths = v
		case "FragmentPath":
			o.FragmentPath = v
		case "ExecStartPre":
			if o.ExecStartPre != "" {
				o.ExecStartPre += "\n"
			}
			o.ExecStartPre += v
		case "NeedDaemonReload":
			o.NeedDaemonReload = v
		}
	}
	return o, nil
}

func countCgroupTasks(cg string) (int, error) {
	if cg == "" {
		return 0, nil
	}
	if !strings.HasPrefix(cg, "/system.slice/") || strings.Contains(cg, "..") {
		return 0, errors.New("writer cgroup outside system.slice")
	}
	root := filepath.Join("/sys/fs/cgroup", filepath.Clean(cg))
	count := 0
	for _, name := range []string{"cgroup.procs", "cgroup.threads"} {
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			return 0, e
		}
		for _, id := range strings.Fields(string(b)) {
			if _, e := strconv.Atoi(id); e != nil {
				return 0, errors.New("invalid cgroup task id")
			}
			count++
		}
	}
	return count, nil
}

func verifyEffectiveHoldGuards(service, serviceDropins, timer, timerDropins, maintenanceRoot string) (bool, error) {
	hold := filepath.Join(maintenanceRoot, maintenance.DefaultUpgradeHoldFile)
	for unit, dropins := range map[string]string{service: serviceDropins, timer: timerDropins} {
		path := filepath.Join(maintenance.DefaultDropInDir, unit+".d", maintenance.DropInFileName)
		loaded := false
		for _, p := range strings.Fields(dropins) {
			if filepath.Clean(p) == filepath.Clean(path) {
				loaded = true
			}
		}
		if !loaded {
			return false, fmt.Errorf("effective hold drop-in not loaded for %s", unit)
		}
		st, e := os.Lstat(path)
		if e != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0644 {
			return false, fmt.Errorf("hold guard missing/unsafe for %s", unit)
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || owner.Gid != 0 {
			return false, fmt.Errorf("hold guard owner invalid for %s", unit)
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return false, e
		}
		text := string(b)
		if !strings.Contains(text, "ConditionPathExists=!"+hold) {
			return false, fmt.Errorf("hold condition missing for %s", unit)
		}
		if strings.HasSuffix(unit, ".service") && !strings.Contains(text, "ExecStartPre=/usr/bin/test ! -f "+hold) {
			return false, fmt.Errorf("ExecStartPre guard missing for %s", unit)
		}
	}
	return true, nil
}

func createOfflineWriterEvidenceDir(stateRoot string) (string, error) {
	root := filepath.Join(stateRoot, "pending-recovery-backups")
	st, e := os.Lstat(root)
	if errors.Is(e, os.ErrNotExist) {
		if e = os.Mkdir(root, 0700); e != nil {
			return "", e
		}
		st, e = os.Lstat(root)
	}
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0700 {
		return "", errors.New("private writer evidence root unsafe")
	}
	return os.MkdirTemp(root, "writer-state-")
}
func writePrivateEvidence(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	_, w := f.Write(b)
	s := f.Sync()
	c := f.Close()
	return errors.Join(w, s, c)
}
func archiveFailedWriter(ctx context.Context, dir string, o, t offlineWriterObservation, tasks int, jobs []string) error {
	b, _ := json.Marshal(struct {
		Writer      offlineWriterObservation `json:"writer"`
		Timer       offlineWriterObservation `json:"timer"`
		CgroupTasks int                      `json:"cgroup_tasks"`
		Jobs        []string                 `json:"jobs"`
	}{o, t, tasks, jobs})
	if e := writePrivateEvidence(filepath.Join(dir, o.Unit+".json"), append(b, '\n')); e != nil {
		return e
	}
	out, e := exec.CommandContext(ctx, "/usr/bin/journalctl", "-u", o.Unit, "-n", "40", "--no-pager", "-o", "short-precise").Output()
	if e != nil {
		return fmt.Errorf("archive unit journal %s: %w", o.Unit, e)
	}
	return writePrivateEvidence(filepath.Join(dir, o.Unit+".journal.txt"), out)
}
func hashBytesLocal(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

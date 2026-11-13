package agentd

import (
	"strings"
	"testing"
)

// These raw show fixtures model omitted timer properties, not archived samples
// from the historical recovery run (its two in-process samples were not saved).
const failedWriterShow = `LoadState=loaded
ActiveState=failed
Result=exit-code
MainPID=0
ControlPID=0
ControlGroup=
InvocationID=service-inv
ExecMainStatus=1
ExecMainStartTimestamp=timestamp
`

const inactiveTimerShow = `LoadState=loaded
ActiveState=inactive
SubState=dead
Result=success
InvocationID=timer-inv
`

func TestOfflineWriterRawShowRecoveryProof(t *testing.T) {
	for service, timer := range recoverableFailedWriters {
		t.Run(service, func(t *testing.T) {
			for _, pids := range []string{"", "MainPID=0\nControlPID=0\nControlGroup=\n"} {
				before := parseOfflineWriterObservation(service, []byte(failedWriterShow))
				timerBefore := parseOfflineWriterObservation(timer, []byte(inactiveTimerShow+pids))
				if pids == "" && (timerBefore.MainPID != "" || timerBefore.ControlPID != "" || timerBefore.ControlGroup != "") {
					t.Fatalf("missing timer properties were normalized: %+v", timerBefore)
				}
				if err := assessQuiescentFailedWriter(before, timerBefore, nil, true); err != nil {
					t.Fatalf("raw show initial proof rejected: %v", err)
				}
				after := parseOfflineWriterObservation(service, []byte(failedWriterShow))
				timerAfter := parseOfflineWriterObservation(timer, []byte(inactiveTimerShow+pids))
				if err := verifyFailedWriterStable(before, after, timerBefore, timerAfter); err != nil {
					t.Fatalf("raw show stable proof rejected: %v", err)
				}
			}
		})
	}
}

func TestOfflineWriterRawShowRejectsUnsafeEvidence(t *testing.T) {
	const service = "urmail-usage-sync.service"
	const timer = "urmail-usage-sync.timer"
	cases := []struct {
		name    string
		writer  bool
		oldLine string
		newLine string
	}{
		{"service main PID missing", true, "MainPID=0\n", ""},
		{"service control PID missing", true, "ControlPID=0\n", ""},
		{"service main PID empty", true, "MainPID=0", "MainPID="},
		{"service control PID empty", true, "ControlPID=0", "ControlPID="},
		{"service main PID nonzero", true, "MainPID=0", "MainPID=123"},
		{"service control PID nonzero", true, "ControlPID=0", "ControlPID=7"},
		{"service cgroup assigned", true, "ControlGroup=", "ControlGroup=/system.slice/writer"},
		{"timer active", false, "ActiveState=inactive", "ActiveState=active"},
		{"timer load missing", false, "LoadState=loaded\n", ""},
		{"timer unloaded", false, "LoadState=loaded", "LoadState=not-found"},
		{"timer main PID nonzero", false, "", "MainPID=123\n"},
		{"timer control PID nonzero", false, "", "ControlPID=7\n"},
		{"timer main PID unknown", false, "", "MainPID=unknown\n"},
		{"timer control PID unknown", false, "", "ControlPID=unknown\n"},
		{"timer cgroup assigned", false, "", "ControlGroup=/system.slice/timer\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writerRaw, timerRaw := failedWriterShow, inactiveTimerShow
			mutate := func(raw string) string {
				if tc.oldLine == "" {
					return raw + tc.newLine
				}
				return strings.Replace(raw, tc.oldLine, tc.newLine, 1)
			}
			if tc.writer {
				writerRaw = mutate(writerRaw)
			} else {
				timerRaw = mutate(timerRaw)
			}
			after := parseOfflineWriterObservation(service, []byte(writerRaw))
			timerAfter := parseOfflineWriterObservation(timer, []byte(timerRaw))
			if err := assessQuiescentFailedWriter(after, timerAfter, nil, true); err == nil {
				t.Fatal("unsafe raw show evidence passed initial proof")
			}
			before := parseOfflineWriterObservation(service, []byte(failedWriterShow))
			timerBefore := parseOfflineWriterObservation(timer, []byte(inactiveTimerShow))
			if err := verifyFailedWriterStable(before, after, timerBefore, timerAfter); err == nil {
				t.Fatal("unsafe raw show evidence passed stability proof")
			}
		})
	}
}

func TestOfflineWriterTimerIdentityAndInvocation(t *testing.T) {
	before := parseOfflineWriterObservation("urmail-usage-sync.service", []byte(failedWriterShow))
	timerBefore := parseOfflineWriterObservation("urmail-usage-sync.timer", []byte(inactiveTimerShow))
	for _, unit := range []string{"unknown.timer", "unknown.service", "urmail-cert-renew.timer", ""} {
		t.Run(unit, func(t *testing.T) {
			timerAfter := parseOfflineWriterObservation(unit, []byte(inactiveTimerShow))
			if err := assessQuiescentFailedWriter(before, timerAfter, nil, true); err == nil {
				t.Fatal("wrong timer identity passed initial proof")
			}
			if err := verifyFailedWriterStable(before, before, timerBefore, timerAfter); err == nil {
				t.Fatal("changed timer identity passed stability proof")
			}
			if err := verifyFailedWriterStable(before, before, timerAfter, timerAfter); err == nil {
				t.Fatal("stable but incorrect timer identity passed stability proof")
			}
		})
	}
	for _, invocation := range []string{"timer-new-inv", ""} {
		raw := strings.Replace(inactiveTimerShow, "InvocationID=timer-inv", "InvocationID="+invocation, 1)
		timerAfter := parseOfflineWriterObservation(timerBefore.Unit, []byte(raw))
		if err := verifyFailedWriterStable(before, before, timerBefore, timerAfter); err == nil {
			t.Fatal("changed or lost timer invocation passed stability proof")
		}
	}
	unknown := parseOfflineWriterObservation("unknown.service", []byte(failedWriterShow))
	if err := verifyFailedWriterStable(unknown, unknown, timerBefore, timerBefore); err == nil {
		t.Fatal("unknown writer passed stability proof")
	}
}

func TestQuiescentFailedWriterPredicate(t *testing.T) {
	service := offlineWriterObservation{Unit: "urmail-usage-sync.service", Load: "loaded", Active: "failed", Result: "exit-code", MainPID: "0", ControlPID: "0", ControlGroup: "", InvocationID: "inv", ExecMainStatus: "1", ExecMainStartTimestamp: "timestamp"}
	timer := offlineWriterObservation{Unit: "urmail-usage-sync.timer", Load: "loaded", Active: "inactive", Result: "success", MainPID: "0", ControlPID: "0", ControlGroup: ""}
	if err := assessQuiescentFailedWriter(service, timer, nil, true); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*offlineWriterObservation, *offlineWriterObservation, *[]string, *bool)
		want   string
	}{
		{"active", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.Active = "active" }, "state/result/invocation evidence incomplete"},
		{"not allowlisted", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.Unit = "postfix.service" }, "not in offline recovery allowlist"},
		{"main pid", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.MainPID = "123" }, "PID/control/cgroup"},
		{"control pid", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.ControlPID = "7" }, "PID/control/cgroup"},
		{"cgroup assigned", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.ControlGroup = "/system.slice/x" }, "PID/control/cgroup"},
		{"service job", func(_, _ *offlineWriterObservation, j *([]string), _ *bool) {
			*j = []string{"1 urmail-usage-sync.service start running"}
		}, "queued systemd job"},
		{"timer job", func(_, _ *offlineWriterObservation, j *([]string), _ *bool) {
			*j = []string{"1 urmail-usage-sync.timer start running"}
		}, "queued systemd job"},
		{"guard missing", func(_, _ *offlineWriterObservation, _ *([]string), g *bool) { *g = false }, "lacks the active hold guard"},
		{"timer active", func(_, t *offlineWriterObservation, _ *([]string), _ *bool) { t.Active = "active" }, "not loaded/inactive"},
		{"unknown result", func(s, _ *offlineWriterObservation, _ *([]string), _ *bool) { s.Result = "" }, "state/result/invocation evidence incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tm := service, timer
			jobs := []string{}
			guard := true
			tc.mutate(&s, &tm, &jobs, &guard)
			err := assessQuiescentFailedWriter(s, tm, jobs, guard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestOfflineWriterStabilityAndSystemdJobs(t *testing.T) {
	before := offlineWriterObservation{Unit: "urmail-usage-sync.service", Load: "loaded", Active: "failed", Result: "exit-code", MainPID: "0", ControlPID: "0", InvocationID: "inv-1", ExecMainStatus: "1", ExecMainStartTimestamp: "timestamp"}
	timerBefore := offlineWriterObservation{Unit: "urmail-usage-sync.timer", Load: "loaded", Active: "inactive", MainPID: "0", ControlPID: "0", InvocationID: "timer-inv"}
	if err := verifyFailedWriterStable(before, before, timerBefore, timerBefore); err != nil {
		t.Fatalf("stable failed writer rejected: %v", err)
	}
	cases := []struct {
		name   string
		writer func(*offlineWriterObservation)
		timer  func(*offlineWriterObservation)
	}{
		{"invocation changed", func(o *offlineWriterObservation) { o.InvocationID = "inv-2" }, nil},
		{"status changed", func(o *offlineWriterObservation) { o.ExecMainStatus = "0" }, nil},
		{"pid appeared", func(o *offlineWriterObservation) { o.MainPID = "99" }, nil},
		{"timer invocation changed", nil, func(o *offlineWriterObservation) { o.InvocationID = "timer-2" }},
		{"timer active", nil, func(o *offlineWriterObservation) { o.Active = "active" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after, timerAfter := before, timerBefore
			if tc.writer != nil {
				tc.writer(&after)
			}
			if tc.timer != nil {
				tc.timer(&timerAfter)
			}
			if err := verifyFailedWriterStable(before, after, timerBefore, timerAfter); err == nil {
				t.Fatal("changed state passed recheck")
			}
		})
	}
	if got := nonemptySystemdJobs([]byte("No jobs running.\n")); len(got) != 0 {
		t.Fatalf("no-jobs status parsed as jobs: %v", got)
	}
	if got := nonemptySystemdJobs([]byte("1 urmail-usage-sync.service start running\n")); len(got) != 1 {
		t.Fatalf("queued job not detected: %v", got)
	}
}

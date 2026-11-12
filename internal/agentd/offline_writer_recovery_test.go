package agentd

import (
	"strings"
	"testing"
)

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

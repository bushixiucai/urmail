package maildnsreport_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/maildnsreport"
)

func boolPtr(b bool) *bool { return &b }

func baseState() maildnsreport.DomainState {
	return maildnsreport.DomainState{
		Domain:           "Example.COM.",
		ReportingAddress: "reports@example.net",
		Records: []maildnsreport.Record{
			{Name: "@", Type: "TXT", Value: "v=spf1 mx -all"},
			{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=quarantine; rua=mailto:dmarc-reports@urmail.cn"},
			{Name: "mail._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=MIGfMA0GCS"},
			{Name: "_mta-sts", Type: "TXT", Value: "v=STSv1; id=20261027000000"},
			{Name: "_smtp._tls", Type: "TXT", Value: "v=TLSRPTv1; rua=mailto:tls-reports@urmail.cn"},
		},
		RequiredDKIMSelectors: []maildnsreport.DKIMSelector{{Name: "mail"}},
		MTASTSWellKnownLive:   boolPtr(true),
	}
}

func checkByID(t *testing.T, r maildnsreport.Report, id string) maildnsreport.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("report has no check %q", id)
	return maildnsreport.Check{}
}

func TestAssessCompleteDomain(t *testing.T) {
	r := maildnsreport.Assess(baseState())
	if !r.Complete {
		t.Fatalf("expected complete report, got:\n%s", r.FormatTable())
	}
	if r.Domain != "example.com" {
		t.Errorf("domain must be normalized, got %q", r.Domain)
	}
	for _, c := range r.Checks {
		if c.Status != maildnsreport.StatusOK && c.Status != maildnsreport.StatusUnknown {
			t.Errorf("check %s = %s (%s)", c.ID, c.Status, c.Detail)
		}
	}
}

func TestAssessTable(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*maildnsreport.DomainState)
		checkID    string
		wantStatus maildnsreport.CheckStatus
		wantInText string // substring expected in the reason
	}{
		{
			name: "missing_spf",
			mutate: func(s *maildnsreport.DomainState) {
				out := s.Records[:0]
				for _, rec := range s.Records {
					if rec.Name == "@" {
						continue
					}
					out = append(out, rec)
				}
				s.Records = out
			},
			checkID: "spf", wantStatus: maildnsreport.StatusMissing, wantInText: "No SPF record",
		},
		{
			name: "wrong_spf_policy",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "@" {
						s.Records[i].Value = "v=spf1 +all"
					}
				}
			},
			checkID: "spf", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "does not match the platform policy",
		},
		{
			name: "multiple_spf_records",
			mutate: func(s *maildnsreport.DomainState) {
				s.Records = append(s.Records, maildnsreport.Record{Name: "@", Type: "TXT", Value: "v=spf1 mx -all"})
			},
			checkID: "spf", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "Multiple SPF records",
		},
		{
			name: "dmarc_policy_none",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "_dmarc" {
						s.Records[i].Value = "v=DMARC1; p=none"
					}
				}
			},
			checkID: "dmarc", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "p=none",
		},
		{
			name: "dmarc_missing_p",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "_dmarc" {
						s.Records[i].Value = "v=DMARC1; rua=mailto:x@y.z"
					}
				}
			},
			checkID: "dmarc", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "no explicit p=",
		},
		{
			name: "dmarc_missing_record",
			mutate: func(s *maildnsreport.DomainState) {
				out := s.Records[:0]
				for _, rec := range s.Records {
					if rec.Name == "_dmarc" {
						continue
					}
					out = append(out, rec)
				}
				s.Records = out
			},
			checkID: "dmarc", wantStatus: maildnsreport.StatusMissing, wantInText: "No DMARC record",
		},
		{
			name: "dmarc_bad_rua",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "_dmarc" {
						s.Records[i].Value = "v=DMARC1; p=reject; rua=mailto:not-an-address"
					}
				}
			},
			checkID: "dmarc", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "not a valid",
		},
		{
			name: "no_tls_rpt",
			mutate: func(s *maildnsreport.DomainState) {
				out := s.Records[:0]
				for _, rec := range s.Records {
					if rec.Name == "_smtp._tls" {
						continue
					}
					out = append(out, rec)
				}
				s.Records = out
			},
			checkID: "tls_rpt", wantStatus: maildnsreport.StatusMissing, wantInText: "No TLS-RPT record",
		},
		{
			name: "tls_rpt_no_rua",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "_smtp._tls" {
						s.Records[i].Value = "v=TLSRPTv1"
					}
				}
			},
			checkID: "tls_rpt", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "no rua=",
		},
		{
			name: "no_mtasts",
			mutate: func(s *maildnsreport.DomainState) {
				out := s.Records[:0]
				for _, rec := range s.Records {
					if rec.Name == "_mta-sts" {
						continue
					}
					out = append(out, rec)
				}
				s.Records = out
			},
			checkID: "mta_sts", wantStatus: maildnsreport.StatusMissing, wantInText: "No MTA-STS record",
		},
		{
			name: "mtasts_empty_id",
			mutate: func(s *maildnsreport.DomainState) {
				for i := range s.Records {
					if s.Records[i].Name == "_mta-sts" {
						s.Records[i].Value = "v=STSv1; id="
					}
				}
			},
			checkID: "mta_sts", wantStatus: maildnsreport.StatusPresentWrong, wantInText: "empty id=",
		},
		{
			name: "missing_dkim_selector",
			mutate: func(s *maildnsreport.DomainState) {
				s.RequiredDKIMSelectors = append(s.RequiredDKIMSelectors, maildnsreport.DKIMSelector{Name: "sep2"})
			},
			checkID: "dkim", wantStatus: maildnsreport.StatusMissing, wantInText: "sep2._domainkey",
		},
		{
			name:    "well_known_not_live",
			mutate:  func(s *maildnsreport.DomainState) { s.MTASTSWellKnownLive = boolPtr(false) },
			checkID: "mta_sts_well_known", wantStatus: maildnsreport.StatusMissing, wantInText: "HTTPS policy is missing",
		},
		{
			name:    "well_known_unprobed",
			mutate:  func(s *maildnsreport.DomainState) { s.MTASTSWellKnownLive = nil },
			checkID: "mta_sts_well_known", wantStatus: maildnsreport.StatusUnknown, wantInText: "not probed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := baseState()
			tc.mutate(&state)
			r := maildnsreport.Assess(state)
			c := checkByID(t, r, tc.checkID)
			if c.Status != tc.wantStatus {
				t.Fatalf("check %s = %s, want %s (%s)", tc.checkID, c.Status, tc.wantStatus, c.Detail)
			}
			if tc.wantInText != "" && !strings.Contains(c.Detail, tc.wantInText) {
				t.Errorf("detail %q must contain %q", c.Detail, tc.wantInText)
			}
			if tc.wantStatus != maildnsreport.StatusOK && r.Complete {
				t.Errorf("report must be incomplete when %s is %s", tc.checkID, tc.wantStatus)
			}
		})
	}
}

func TestFormatTableStable(t *testing.T) {
	r := maildnsreport.Assess(baseState())
	table := r.FormatTable()
	if !strings.HasPrefix(table, "DNS policy report for example.com: COMPLETE") {
		t.Errorf("unexpected table header:\n%s", table)
	}
	if table != maildnsreport.Assess(baseState()).FormatTable() {
		t.Errorf("FormatTable must be deterministic")
	}
}

// --- MTA-STS well-known body ---

func TestRenderWellKnownGolden(t *testing.T) {
	p := maildnsreport.MTASTSPolicy{
		ID:   "20261027000000",
		Mode: maildnsreport.MTASTSEnforce,
		MX:   []string{"Box.URMail.CN.", "mx2.urmail.cn"},
	}
	body, err := maildnsreport.RenderWellKnown(p)
	if err != nil {
		t.Fatalf("RenderWellKnown: %v", err)
	}
	golden := filepath.Join("testdata", "golden", "mta-sts.txt")
	if *update {
		if err := os.WriteFile(golden, []byte(body), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	expected, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(expected) != body {
		t.Errorf("well-known mismatch:\n--- Expected ---\n%s\n--- Got ---\n%s", expected, body)
	}
}

func TestRenderWellKnownValid(t *testing.T) {
	body, err := maildnsreport.RenderWellKnown(maildnsreport.MTASTSPolicy{
		ID: "x", Mode: maildnsreport.MTASTSTesting, MX: []string{"mx.example"}, MaxAgeSeconds: 604800,
	})
	if err != nil {
		t.Fatalf("testing mode: %v", err)
	}
	want := "version: STSv1\nmode: testing\nmx: mx.example\nmax_age: 604800\n"
	if body != want {
		t.Errorf("got %q, want %q", body, want)
	}
}

func TestRenderWellKnownRejectsInvalid(t *testing.T) {
	cases := make([]maildnsreport.MTASTSPolicy, 0, 8)
	cases = append(cases,
		maildnsreport.MTASTSPolicy{ID: "", Mode: maildnsreport.MTASTSEnforce, MX: []string{"mx.example"}},
		maildnsreport.MTASTSPolicy{ID: strings.Repeat("x", 33), Mode: maildnsreport.MTASTSEnforce, MX: []string{"mx.example"}},
		maildnsreport.MTASTSPolicy{ID: "x", Mode: "bogus", MX: []string{"mx.example"}},
		maildnsreport.MTASTSPolicy{ID: "x", Mode: maildnsreport.MTASTSEnforce, MX: nil},
		maildnsreport.MTASTSPolicy{ID: "x", Mode: maildnsreport.MTASTSEnforce, MX: []string{"two hosts"}},
		maildnsreport.MTASTSPolicy{ID: "x", Mode: maildnsreport.MTASTSEnforce, MX: []string{"bad..host"}},
		maildnsreport.MTASTSPolicy{ID: "x", Mode: maildnsreport.MTASTSEnforce, MX: []string{"mx.example"}, MaxAgeSeconds: 0},
	)
	// max_age 0 selects the default and must render.
	if _, err := maildnsreport.RenderWellKnown(cases[len(cases)-1]); err != nil {
		t.Errorf("default max_age must render, got %v", err)
	}
	cases[len(cases)-1].MaxAgeSeconds = 31557601
	cases = append(cases, maildnsreport.MTASTSPolicy{ID: "x", Mode: maildnsreport.MTASTSEnforce, MX: []string{"mx.example"}, MaxAgeSeconds: -1})
	for _, p := range cases {
		if _, err := maildnsreport.RenderWellKnown(p); !errors.Is(err, maildnsreport.ErrInvalidMTASTS) {
			t.Errorf("policy %+v must be rejected, got %v", p, err)
		}
	}
}

func TestRequiredRecords(t *testing.T) {
	recs, err := maildnsreport.RequiredRecords("example.com", "20261027000000", "enforce",
		[]string{"box.urmail.cn"}, "tls-reports@urmail.cn")
	if err != nil {
		t.Fatalf("RequiredRecords: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].Name != "_mta-sts" || recs[0].Value != "v=STSv1; id=20261027000000" {
		t.Errorf("mta-sts record wrong: %+v", recs[0])
	}
	if recs[1].Name != "_smtp._tls" || recs[1].Value != "v=TLSRPTv1; rua=mailto:tls-reports@urmail.cn" {
		t.Errorf("tls-rpt record wrong: %+v", recs[1])
	}
	if _, err := maildnsreport.RequiredRecords("example.com", "", "enforce", []string{"mx"}, ""); !errors.Is(err, maildnsreport.ErrInvalidMTASTS) {
		t.Errorf("empty policy id must be rejected, got %v", err)
	}
	if _, err := maildnsreport.RequiredRecords("", "id", "enforce", []string{"mx"}, ""); !errors.Is(err, maildnsreport.ErrInvalidMTASTS) {
		t.Errorf("empty domain must be rejected, got %v", err)
	}
}

package maildnsreport

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func syntheticTLSReport(now time.Time) []byte {
	raw, _ := json.Marshal(map[string]any{"organization-name": "Synthetic sender", "report-id": "unique-synthetic-report", "contact-info": "must-not-be-persisted@example.test", "date-range": map[string]string{"start-datetime": now.Add(-24 * time.Hour).Format(time.RFC3339), "end-datetime": now.Format(time.RFC3339)}, "policies": []any{map[string]any{"policy": map[string]string{"policy-type": "sts", "policy-domain": "recipient.example"}, "summary": map[string]int64{"total-successful-session-count": 20, "total-failure-session-count": 2}, "failure-details": []any{map[string]any{"result-type": "certificate-expired", "failed-session-count": 2, "sending-mta-ip": "192.0.2.7", "additional-information": "sensitive user-controlled text"}}}}})
	return raw
}
func TestTLSReportStrictBoundedDomainAndPrivacy(t *testing.T) {
	now := time.Now().UTC()
	raw := syntheticTLSReport(now)
	for _, compress := range []bool{false, true} {
		data := raw
		if compress {
			var buffer bytes.Buffer
			writer := gzip.NewWriter(&buffer)
			_, _ = writer.Write(raw)
			_ = writer.Close()
			data = buffer.Bytes()
		}
		result, err := ParseTLSReport(data, compress, "recipient.example", now)
		if err != nil || result.Successful != 20 || result.Failed != 2 || result.Failures["certificate-expired"] != 2 {
			t.Fatalf("summary %+v %v", result, err)
		}
		encoded, _ := json.Marshal(result)
		for _, secret := range []string{"sensitive", "192.0.2.7", "@example.test", "Synthetic sender"} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("persisted report identifiers %s", encoded)
			}
		}
		if _, err = ParseTLSReport(data, compress, "other.example", now); err == nil {
			t.Fatal("cross-domain report accepted")
		}
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), raw...), []byte(strings.Replace(string(raw), `"failed-session-count":2`, `"failed-session-count":3`, 1)), []byte(strings.Replace(string(raw), `"total-successful-session-count":20`, `"total-successful-session-count":-1`, 1)), make([]byte, MaxTLSReportWireBytes+1)} {
		if _, err := ParseTLSReport(bad, false, "recipient.example", now); err == nil {
			t.Fatal("invalid report accepted")
		}
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), MaxTLSReportBytes+1))
	_ = writer.Close()
	if _, err := ParseTLSReport(compressed.Bytes(), true, "recipient.example", now); err == nil {
		t.Fatal("compressed expansion cap bypassed")
	}
	if _, err := ParseTLSReport(raw, false, "recipient.example", now.Add(100*24*time.Hour)); err == nil {
		t.Fatal("expired report accepted")
	}
}
func TestTransportPolicyIdentifiersAndHTTPSReportURI(t *testing.T) {
	for _, value := range []string{"https://reports.example/api/v1/tls-reports/recipient.example", "mailto:reports@example.test"} {
		if !ValidTLSReportURI(value) {
			t.Fatalf("valid URI %s", value)
		}
	}
	for _, value := range []string{"http://reports.example/", "https://user:secret@reports.example/", "https://reports.example/#token", "mailto:not-a-mailbox", "https://reports.example/\r\nx:1"} {
		if ValidTLSReportURI(value) {
			t.Fatalf("invalid URI %s", value)
		}
	}
	if got := TLSRPTRecord("https://reports.example/tls"); got != "v=TLSRPTv1; rua=https://reports.example/tls" {
		t.Fatal(got)
	}
	policy := MTASTSPolicy{ID: "bad;record", Mode: MTASTSTesting, MX: []string{"mail.example.test"}}
	if _, err := RenderWellKnown(policy); err == nil {
		t.Fatal("record delimiter in ID accepted")
	}
	policy.ID = "stable1"
	policy.MX = []string{"mx.example;injection"}
	if _, err := RenderWellKnown(policy); err == nil {
		t.Fatal("invalid MX accepted")
	}
}

func TestMTASTSTXTRequiresRFCFieldDelimiters(t *testing.T) {
	for _, record := range []string{"v=STSv1; id=stable1", "v=STSv1; id=stable1;", "v=STSv1; extension=ok; id=stable1"} {
		id, ok := MTASTSRecordID(record)
		if !ok || id != "stable1" {
			t.Fatalf("standard TXT rejected %s", record)
		}
	}
	for _, record := range []string{"v=STSv1 id=stable1", "v=STSv1;id=one;id=two", "v=STSv1;v=STSv2;id=stable1", "v=STSv1;id=bad value", "v=STSv1;;id=stable1"} {
		if _, ok := MTASTSRecordID(record); ok {
			t.Fatalf("invalid TXT accepted %s", record)
		}
	}
}

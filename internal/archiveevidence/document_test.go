package archiveevidence

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestOfflinePackageVerificationRejectsChangedMissingAndExtraFiles(t *testing.T) {
	raw := []byte("From: original@example.test\r\n\r\nExact original\r\n")
	sum := sha256.Sum256(raw)
	doc := Document{Title: "Bounded investigation", Purpose: "Original approved source", ApprovedAt: "2026-10-27T00:00:00Z", GeneratedAt: "2026-10-27T00:00:01Z", Version: 1, TenantID: 1, CaseID: 2, CaseVersion: 3, ApprovalID: 4, RequesterID: 5, ApproverID: 6, ExporterID: 5, SourceDigest: hex.EncodeToString(sum[:]), Files: []File{{EntryID: 7, Name: "messages/7.eml", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))}}}
	manifest, err := EncodeDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	anchor := sha256.Sum256(manifest)
	build := func(body, guide []byte, omit, extra, duplicate bool) []byte {
		var output bytes.Buffer
		writer := zip.NewWriter(&output)
		add := func(name string, payload []byte) {
			t.Helper()
			entry, e := writer.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = entry.Write(payload); e != nil {
				t.Fatal(e)
			}
		}
		add("manifest.json", manifest)
		add("VERIFYING.txt", guide)
		if !omit {
			add(doc.Files[0].Name, body)
		}
		if extra {
			add("../outside", []byte("escape"))
		}
		if duplicate {
			add(doc.Files[0].Name, body)
		}
		if e := writer.Close(); e != nil {
			t.Fatal(e)
		}
		return output.Bytes()
	}
	for _, tc := range []struct {
		name                          string
		body, guide                   []byte
		omit, extra, duplicate, valid bool
	}{
		{name: "exact", body: raw, guide: []byte(VerificationGuide), valid: true},
		{name: "tampered-original", body: []byte("changed"), guide: []byte(VerificationGuide)},
		{name: "missing-original", guide: []byte(VerificationGuide), omit: true},
		{name: "extra-traversal-member", body: raw, guide: []byte(VerificationGuide), extra: true},
		{name: "duplicate-member", body: raw, guide: []byte(VerificationGuide), duplicate: true},
		{name: "changed-guide", body: raw, guide: []byte("malicious instructions")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := build(tc.body, tc.guide, tc.omit, tc.extra, tc.duplicate)
			_, e := VerifyPackage(bytes.NewReader(data), int64(len(data)), hex.EncodeToString(anchor[:]))
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, e)
			}
		})
	}
	good := build(raw, []byte(VerificationGuide), false, false, false)
	if _, err = VerifyPackage(bytes.NewReader(good), int64(len(good)), hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("wrong independent manifest anchor accepted")
	}
	if _, err = VerifyPackage(bytes.NewReader(good), int64(len(good)), ""); err == nil {
		t.Fatal("verification without independent expected anchor accepted")
	}
}

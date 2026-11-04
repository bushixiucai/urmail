package backupauto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// v1 is byte-identical to the historical cmd/urmailctl manifest format.
const v1Manifest = `{"version":1,"scope":"metadata-only","consistent_maintenance":true,
"created_at":"2026-01-01T00:00:00Z","artifacts":[{"name":"urmail-x.dump",
"sha256":"abc","size":3,"mode":384,"uid":0,"gid":0}]}`

func TestDecodeManifestV1StillAccepted(t *testing.T) {
	m, err := DecodeManifest(strings.NewReader(v1Manifest))
	if err != nil {
		t.Fatalf("v1 manifest must remain restorable: %v", err)
	}
	if m.Version != 1 || len(m.Artifacts) != 1 || m.Artifacts[0].Name != "urmail-x.dump" {
		t.Fatalf("v1 decode mismatch: %+v", m)
	}
}

func TestDecodeManifestV2RequiresEncryptionMetadata(t *testing.T) {
	base := `{"version":2,"scope":"local-mail-and-metadata","consistent_maintenance":true,
"created_at":"2026-01-01T00:00:00Z","encryption":{"algorithm":"age","recipient":"age1x"},
"artifacts":[{"name":"urmail-x.dump","sha256":"abc","size":3,"mode":384,
"encrypted_name":"urmail-x.dump.age","encrypted_sha256":"def","encrypted_size":9,
"encryption":"age","kind":"pg_dump"}]}`
	m, err := DecodeManifest(strings.NewReader(base))
	if err != nil {
		t.Fatalf("valid v2 manifest rejected: %v", err)
	}
	if m.Encryption == nil || m.Encryption.Algorithm != "age" {
		t.Fatalf("v2 encryption metadata lost: %+v", m)
	}

	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "encryption") },
		func(m map[string]any) {
			delete(m["artifacts"].([]any)[0].(map[string]any), "encrypted_sha256")
		},
		func(m map[string]any) {
			delete(m["artifacts"].([]any)[0].(map[string]any), "encrypted_name")
		},
	} {
		broken := decodeAny(t, base)
		mutate(broken)
		if _, err := DecodeManifest(strings.NewReader(encodeAny(t, broken))); err == nil {
			t.Fatal("v2 manifest without complete encryption metadata must be rejected")
		}
	}
}

func TestDecodeManifestRejectsUnknownVersion(t *testing.T) {
	if _, err := DecodeManifest(strings.NewReader(`{"version":99}`)); err == nil {
		t.Fatal("unknown manifest version must fail closed")
	}
	if _, err := DecodeManifest(strings.NewReader(`{"version":0}`)); err == nil {
		t.Fatal("missing manifest version must fail closed")
	}
}

func TestPlanBackupSetCoversCollaborationDataInDump(t *testing.T) {
	entries, err := PlanBackupSet(PlannerConfig{OutDir: "/var/backups/urmail", Stamp: StampFor(time.Unix(0, 0).UTC()), IncludeMail: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Kind] = true
	}
	if !kinds[KindPgDump] || !kinds[KindConfigTar] || !kinds[KindMailTar] {
		t.Fatalf("complete plan must cover dump+config+mail, got %v", entries)
	}
	// Contacts/calendars live in PG (migrations 0108-0111): no separate
	// collaboration artifact exists by design.
	for _, e := range entries {
		if e.Kind == "collab_tar" {
			t.Fatal("collaboration data must come from the PG dump, not a separate archive")
		}
	}
}

func TestPlanBackupSetWithoutMailIsMetadataOnly(t *testing.T) {
	entries, err := PlanBackupSet(PlannerConfig{OutDir: "/var/backups/urmail", Stamp: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("metadata-only plan is dump+config, got %v", entries)
	}
}

func TestEncryptedNameAndPrefix(t *testing.T) {
	if EncryptedName("a.dump") != "a.dump.age" {
		t.Fatal("unexpected encrypted name")
	}
	if OffsitePrefix("", "a") != "a" || OffsitePrefix("p/", "/a") != "p/a" || OffsitePrefix("p", "a") != "p/a" {
		t.Fatal("unexpected prefix join")
	}
}

func decodeAny(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := jsonUnmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func encodeAny(t *testing.T, v any) string {
	t.Helper()
	raw, err := jsonMarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }

func TestDecodeManifestV3(t *testing.T) {
	v3Raw := `{"version":3,"layout_version":1,"scope":"local-mail-and-metadata","consistent_maintenance":true,
"created_at":"2026-01-01T00:00:00Z",
"slots":{"mail/storage":{"artifact":"urmail-mail-x.tar.gz","archive_path":"mnt/maildata/mail","target_path":"/mnt/maildata/mail","kind":"dir"}},
"artifacts":[{"name":"urmail-mail-x.tar.gz","sha256":"abc","size":3,"mode":384,"uid":0,"gid":0}]}`
	m, err := DecodeManifest(strings.NewReader(v3Raw))
	if err != nil {
		t.Fatalf("v3 manifest must be accepted: %v", err)
	}
	if m.Version != ManifestVersion3 || m.LayoutVersion != LayoutVersion1 || len(m.Slots) != 1 {
		t.Fatalf("v3 decode mismatch: %+v", m)
	}
	slot, ok := m.Slots[SlotMailStorage]
	if !ok || slot.ArchivePath != "mnt/maildata/mail" {
		t.Fatalf("v3 slot missing or wrong: %+v", slot)
	}

	badLayout := `{"version":3,"layout_version":0,"scope":"local-mail-and-metadata","slots":{"mail/storage":{"artifact":"a","archive_path":"b"}}}`
	if _, err := DecodeManifest(strings.NewReader(badLayout)); err == nil {
		t.Fatal("v3 with layout_version 0 must be rejected")
	}

	noSlots := `{"version":3,"layout_version":1,"scope":"local-mail-and-metadata"}`
	if _, err := DecodeManifest(strings.NewReader(noSlots)); err == nil {
		t.Fatal("v3 without slots must be rejected")
	}
}

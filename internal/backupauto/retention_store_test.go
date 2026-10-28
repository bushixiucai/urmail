package backupauto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

type retentionStoreFixture struct {
	*memoryStore
	deletes        []string
	manifestReads  map[string]int
	changeManifest string
	failDelete     string
}

func (s *retentionStoreFixture) Delete(ctx context.Context, name string) error {
	s.deletes = append(s.deletes, name)
	if name == s.failDelete {
		return errors.New("controlled delete refusal")
	}
	return s.memoryStore.Delete(ctx, name)
}
func (s *retentionStoreFixture) Download(ctx context.Context, name string, w io.Writer) error {
	if strings.HasSuffix(name, ".manifest.json") {
		s.manifestReads[name]++
		if name == s.changeManifest && s.manifestReads[name] > 1 {
			s.mu.Lock()
			raw := append([]byte(nil), s.objects[name]...)
			s.mu.Unlock()
			raw = bytes.Replace(raw, []byte("\n"), []byte(" "), 1)
			_, e := w.Write(raw)
			return e
		}
	}
	return s.memoryStore.Download(ctx, name, w)
}
func newRetentionStore() *retentionStoreFixture {
	return &retentionStoreFixture{memoryStore: newMemoryStore(), manifestReads: map[string]int{}}
}
func retentionGenerationFixture(t *testing.T, s *retentionStoreFixture, recipient string, created time.Time) (string, *Manifest, string) {
	t.Helper()
	stamp := StampFor(created)
	m := &Manifest{Version: 2, Scope: ScopeComplete, Consistent: true, CreatedAt: created, Encryption: &EncryptionInfo{Algorithm: EncryptionAlgorithmAge, Recipient: recipient}}
	entries, _ := PlanBackupSet(PlannerConfig{OutDir: t.TempDir(), Stamp: stamp, IncludeMail: true})
	r, e := ParseRecipient(recipient)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		raw := []byte("synthetic complete-generation artifact " + entry.Kind)
		var cipher bytes.Buffer
		if e = EncryptTo(&cipher, bytes.NewReader(raw), r); e != nil {
			t.Fatal(e)
		}
		a := Artifact{Name: entry.Name, Kind: entry.Kind, Size: int64(len(raw)), SHA256: sha256Hex(raw), Mode: 0o600, EncryptedName: EncryptedName(entry.Name), EncryptedSize: int64(cipher.Len()), EncryptedSHA256: sha256Hex(cipher.Bytes()), Algorithm: EncryptionAlgorithmAge}
		m.Artifacts = append(m.Artifacts, a)
		if e = s.Upload(t.Context(), a.EncryptedName, bytes.NewReader(cipher.Bytes()), int64(cipher.Len())); e != nil {
			t.Fatal(e)
		}
	}
	encoded, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	text := string(encoded) + "\n"
	name := ManifestName(stamp)
	if e = s.Upload(t.Context(), name, strings.NewReader(text), int64(len(text))); e != nil {
		t.Fatal(e)
	}
	return name, m, text
}
func retentionWorkerFixture(t *testing.T) (*Worker, *retentionStoreFixture, *age.X25519Identity, string, *Manifest) {
	t.Helper()
	s := newRetentionStore()
	identity, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	now := mustTime(t, "2026-10-28T12:00:00Z")
	name, current, raw := retentionGenerationFixture(t, s, identity.Recipient().String(), now.Add(-time.Hour))
	w := &Worker{Store: s, Recipient: identity.Recipient().String(), Retention: RetentionPolicy{KeepDaily: 1}, Now: func() time.Time { return now }, manifestName: name, manifestRaw: raw}
	return w, s, identity, name, current
}
func TestRetentionDownloadsCompleteAGEGenerationsAndDeletesOnlyOwnedObjects(t *testing.T) {
	w, s, identity, anchor, current := retentionWorkerFixture(t)
	oldName, old, _ := retentionGenerationFixture(t, s, identity.Recipient().String(), w.now().Add(-72*time.Hour))
	unknown := strings.TrimSuffix(oldName, ".manifest.json") + ".unknown.age"
	s.objects[unknown] = []byte("unknown must remain")
	count, plan, e := w.applyRetention(t.Context())
	if e != nil || count != 4 || len(plan.Delete) != 1 {
		t.Fatal(count, plan, e)
	}
	if len(s.deletes) != 4 || s.deletes[3] != oldName {
		t.Fatal("manifest must be removed last", s.deletes)
	}
	for _, a := range old.Artifacts {
		if _, exists := s.objects[a.EncryptedName]; exists {
			t.Fatal("ciphertext orphaned", a.EncryptedName)
		}
	}
	if _, ok := s.objects[anchor]; !ok {
		t.Fatal("fresh anchor removed")
	}
	if _, ok := s.objects[unknown]; !ok {
		t.Fatal("unknown object removed")
	}
	results, e := VerifyOffsite(t.Context(), s, current, []age.Identity{identity})
	if e != nil || len(results) != 3 {
		t.Fatal(results, e)
	}
	for _, r := range results {
		if !r.OK() {
			t.Fatal("retained recovery ciphertext failed independent actual-age decrypt", r)
		}
	}
}
func TestRetentionRefusesCorruptMissingFutureAndCrossGenerationEvidence(t *testing.T) {
	for _, kind := range []string{"malformed", "missing_artifact", "cipher_corrupt", "size32", "future", "time_mismatch", "missing_kind", "cross_generation", "duplicate_artifact", "wrong_scope", "unknown_version", "wrong_age", "invalid_recipient", "oversize_manifest"} {
		t.Run(kind, func(t *testing.T) {
			w, s, identity, _, _ := retentionWorkerFixture(t)
			oldName, _, _ := retentionGenerationFixture(t, s, identity.Recipient().String(), w.now().Add(-72*time.Hour))
			_ = oldName
			stampTime := w.now().Add(-30 * time.Minute)
			if kind == "future" {
				stampTime = w.now().Add(time.Hour)
			}
			name, m, _ := retentionGenerationFixture(t, s, identity.Recipient().String(), stampTime)
			switch kind {
			case "malformed":
				s.objects[name] = []byte("not JSON")
			case "missing_artifact":
				delete(s.objects, m.Artifacts[2].EncryptedName)
			case "cipher_corrupt":
				s.objects[m.Artifacts[0].EncryptedName][0] ^= 0xff
			case "size32":
				m.Artifacts[0].EncryptedSize = 32
			case "time_mismatch":
				m.CreatedAt = m.CreatedAt.Add(-time.Hour)
			case "missing_kind":
				m.Artifacts[0].Kind = ""
			case "cross_generation":
				m.Artifacts[0].EncryptedName = "urmail-20261025T110000Z.dump.age"
			case "duplicate_artifact":
				m.Artifacts[1] = m.Artifacts[0]
			case "wrong_scope":
				m.Scope = "metadata-only"
			case "unknown_version":
				m.Version = 3
			case "wrong_age":
				m.Artifacts[0].Algorithm = "other"
			case "invalid_recipient":
				m.Encryption.Recipient = "invalid"
			case "oversize_manifest":
				s.objects[name] = bytes.Repeat([]byte(" "), retentionManifestBytes+1)
			}
			if kind != "malformed" && kind != "oversize_manifest" {
				raw, _ := json.Marshal(m)
				s.objects[name] = raw
			}
			count, _, e := w.applyRetention(t.Context())
			if e == nil || count != 0 || len(s.deletes) != 0 {
				t.Fatal("bad evidence authorized deletion", kind, count, e, s.deletes)
			}
		})
	}
}
func TestRetentionBudgetAndChangedManifestNeverAuthorizePartialPruning(t *testing.T) {
	for _, kind := range []string{"no_anchor", "byte_budget", "object_budget", "changed_manifest", "delete_failure"} {
		t.Run(kind, func(t *testing.T) {
			w, s, identity, anchor, _ := retentionWorkerFixture(t)
			old, _, _ := retentionGenerationFixture(t, s, identity.Recipient().String(), w.now().Add(-72*time.Hour))
			switch kind {
			case "no_anchor":
				w.manifestRaw = ""
			case "byte_budget":
				w.RetentionMaxVerifyBytes = 100
			case "object_budget":
				for n := 0; n < retentionMaxObjects; n++ {
					s.objects[fmt.Sprintf("unknown-%d", n)] = []byte("preserve")
				}
			case "changed_manifest":
				s.changeManifest = old
			case "delete_failure":
				s.failDelete = strings.TrimSuffix(old, ".manifest.json") + ".dump.age"
			}
			count, _, e := w.applyRetention(t.Context())
			if e == nil {
				t.Fatal("unsafe retention did not refuse", kind)
			}
			if kind != "delete_failure" && (count != 0 || len(s.deletes) != 0) {
				t.Fatal("partial verification deleted objects", kind, count, s.deletes)
			}
			if _, ok := s.objects[anchor]; !ok {
				t.Fatal("unique complete recovery anchor deleted")
			}
			if kind == "delete_failure" {
				if _, ok := s.objects[old]; !ok {
					t.Fatal("incomplete delete removed manifest first")
				}
			}
		})
	}
}
func TestPlanRetentionRejectsFutureVerifiedCandidate(t *testing.T) {
	now := mustTime(t, "2026-10-28T12:00:00Z")
	plan := PlanRetention([]RetentionCandidate{{ID: "valid", CreatedAt: now.Add(-time.Hour), Verified: true}, {ID: "fake_future", CreatedAt: now.Add(time.Hour), Verified: true}}, RetentionPolicy{}, now)
	if len(plan.Delete) != 0 || len(plan.Refusals) == 0 || len(plan.Keep) != 2 {
		t.Fatal("future timestamp established fresh coverage", plan)
	}
}

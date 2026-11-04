package backupauto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const retentionMaxObjects = 4096
const retentionMaxGenerations = 256
const retentionManifestBytes = 64 << 10
const retentionReadBytes int64 = 1 << 40
const retentionTimeout = time.Hour

var retentionManifestName = regexp.MustCompile(`^urmail-(\d{8}T\d{6}Z)\.manifest\.json$`)
var retentionHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

type retentionGeneration struct {
	candidate    RetentionCandidate
	manifestName string
	rawHash      string
	objects      []string
}
type retentionBudget struct{ remaining int64 }
type retentionWriter struct {
	target    io.Writer
	remaining int64
	budget    *retentionBudget
	count     int64
	prefix    []byte
}

func (w *retentionWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining || int64(len(p)) > w.budget.remaining {
		return 0, fmt.Errorf("retention download exceeds declared size or verification budget")
	}
	n, e := w.target.Write(p)
	w.remaining -= int64(n)
	w.budget.remaining -= int64(n)
	w.count += int64(n)
	if len(w.prefix) < len("age-encryption.org/v1\n") {
		need := len("age-encryption.org/v1\n") - len(w.prefix)
		if need > n {
			need = n
		}
		w.prefix = append(w.prefix, p[:need]...)
	}
	return n, e
}
func retentionRead(ctx context.Context, store OffsiteStore, object RemoteObject, budget *retentionBudget) ([]byte, error) {
	if object.Size <= 0 || object.Size > retentionManifestBytes || object.Size > budget.remaining {
		return nil, fmt.Errorf("retention manifest size/budget invalid")
	}
	var buf bytes.Buffer
	writer := &retentionWriter{target: &buf, remaining: object.Size, budget: budget}
	if e := store.Download(ctx, object.Name, writer); e != nil {
		return nil, e
	}
	if writer.count != object.Size {
		return nil, fmt.Errorf("retention manifest listing size changed")
	}
	return buf.Bytes(), nil
}
func retentionManifest(raw []byte, stamp string, now time.Time) (*Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var m Manifest
	if e := decoder.Decode(&m); e != nil {
		return nil, e
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("retention manifest trailing data")
	}
	if (m.Version != ManifestVersion2 && m.Version != ManifestVersion3) || m.Scope != ScopeComplete || !m.Consistent || m.CreatedAt.IsZero() || m.CreatedAt.After(now) || StampFor(m.CreatedAt) != stamp || m.Encryption == nil || m.Encryption.Algorithm != EncryptionAlgorithmAge || len(m.Artifacts) != 3 {
		return nil, fmt.Errorf("retention requires a complete nonfuture stamp-bound encrypted manifest")
	}
	if m.Version == ManifestVersion3 {
		if m.LayoutVersion != LayoutVersion1 || len(m.Slots) == 0 {
			return nil, fmt.Errorf("retention requires valid v3 layout and slots")
		}
	}
	if _, e := ParseRecipient(m.Encryption.Recipient); e != nil {
		return nil, e
	}
	expected := map[string]string{"urmail-" + stamp + ".dump": KindPgDump, "urmail-config-" + stamp + ".tar.gz": KindConfigTar, "urmail-mail-" + stamp + ".tar.gz": KindMailTar}
	for _, a := range m.Artifacts {
		kind, ok := expected[a.Name]
		if !ok || a.Kind != kind || a.Size <= 0 || a.Mode != 0o600 || !retentionHash.MatchString(a.SHA256) || a.EncryptedName != EncryptedName(a.Name) || a.Algorithm != EncryptionAlgorithmAge || a.EncryptedSize <= 0 || !retentionHash.MatchString(a.EncryptedSHA256) {
			return nil, fmt.Errorf("retention artifact identity, kind or integrity metadata invalid")
		}
		delete(expected, a.Name)
	}
	if len(expected) != 0 {
		return nil, fmt.Errorf("retention required artifacts missing")
	}
	return &m, nil
}
func (w *Worker) inspectRetentionGeneration(ctx context.Context, object RemoteObject, stamp string, inventory map[string]RemoteObject, budget *retentionBudget) (retentionGeneration, error) {
	out := retentionGeneration{manifestName: object.Name}
	raw, e := retentionRead(ctx, w.Store, object, budget)
	if e != nil {
		return out, e
	}
	m, e := retentionManifest(raw, stamp, w.now())
	if e != nil {
		return out, e
	}
	out.candidate = RetentionCandidate{ID: strings.TrimSuffix(object.Name, ".manifest.json"), CreatedAt: m.CreatedAt, Verified: true}
	out.rawHash = sha256Hex(raw)
	for _, a := range m.Artifacts {
		remote, ok := inventory[a.EncryptedName]
		if !ok || remote.Size != a.EncryptedSize || remote.Size > budget.remaining {
			return out, fmt.Errorf("retention ciphertext missing or size/budget invalid")
		}
		h := sha256.New()
		writer := &retentionWriter{target: h, remaining: a.EncryptedSize, budget: budget}
		if e = w.Store.Download(ctx, a.EncryptedName, writer); e != nil {
			return out, e
		}
		if writer.count != a.EncryptedSize || hex.EncodeToString(h.Sum(nil)) != a.EncryptedSHA256 || string(writer.prefix) != "age-encryption.org/v1\n" {
			return out, fmt.Errorf("retention ciphertext integrity/age envelope mismatch")
		}
		out.objects = append(out.objects, a.EncryptedName)
	}
	sort.Strings(out.objects)
	// Remove the manifest last so a partial delete remains visibly incomplete.
	out.objects = append(out.objects, object.Name)
	return out, nil
}

// applyRetention verifies complete ciphertext sets before the pure planner can
// authorize pruning. Unknown objects and invalid generations are never deleted.
// The worker cannot decrypt without the independently protected age identity;
// plaintext restore verification remains the operator's VerifyOffsite duty.
func (w *Worker) applyRetention(ctx context.Context) (int, RetentionPlan, error) {
	timeout := w.RetentionVerifyTimeout
	if timeout <= 0 {
		timeout = retentionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if w.manifestName == "" || w.manifestRaw == "" {
		return 0, RetentionPlan{}, fmt.Errorf("retention lacks a freshly verified current-run anchor")
	}
	objects, e := w.Store.List(ctx)
	if e != nil {
		return 0, RetentionPlan{}, fmt.Errorf("list offsite: %w", e)
	}
	if len(objects) > retentionMaxObjects {
		return 0, RetentionPlan{}, fmt.Errorf("retention object count exceeds verification budget")
	}
	inventory := map[string]RemoteObject{}
	manifests := []RemoteObject{}
	for _, object := range objects {
		if _, exists := inventory[object.Name]; exists {
			return 0, RetentionPlan{}, fmt.Errorf("retention inventory has duplicate identities")
		}
		inventory[object.Name] = object
		if retentionManifestName.MatchString(object.Name) {
			manifests = append(manifests, object)
		}
	}
	if len(manifests) > retentionMaxGenerations {
		return 0, RetentionPlan{}, fmt.Errorf("retention generation count exceeds verification budget")
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Name < manifests[j].Name })
	limit := w.RetentionMaxVerifyBytes
	if limit <= 0 {
		limit = retentionReadBytes
	}
	budget := &retentionBudget{remaining: limit}
	generations := map[string]retentionGeneration{}
	candidates := []RetentionCandidate{}
	anchored := false
	for _, object := range manifests {
		match := retentionManifestName.FindStringSubmatch(object.Name)
		stamp := match[1]
		if _, err := time.Parse("20060102T150405Z", stamp); err != nil {
			return 0, RetentionPlan{}, fmt.Errorf("retention generation timestamp invalid")
		}
		generation, err := w.inspectRetentionGeneration(ctx, object, stamp, inventory, budget)
		if err != nil {
			return 0, RetentionPlan{}, fmt.Errorf("retention refused %s: %w", object.Name, err)
		}
		if object.Name == w.manifestName {
			if generation.rawHash != sha256Hex([]byte(w.manifestRaw)) {
				return 0, RetentionPlan{}, fmt.Errorf("current-run manifest changed offsite")
			}
			anchored = true
		}
		candidates = append(candidates, generation.candidate)
		generations[generation.candidate.ID] = generation
	}
	if !anchored {
		return 0, RetentionPlan{}, fmt.Errorf("fresh current-run complete generation not found")
	}
	plan := PlanRetention(candidates, w.Retention, w.now())
	if len(plan.Refusals) > 0 || len(plan.Delete) == 0 {
		return 0, plan, nil
	}
	// Finish the whole bounded verification before the first delete. Re-read each
	// deletion manifest to fence changes observed after the inventory snapshot.
	for _, id := range plan.Delete {
		generation := generations[id]
		raw, err := retentionRead(ctx, w.Store, inventory[generation.manifestName], budget)
		if err != nil || sha256Hex(raw) != generation.rawHash {
			return 0, plan, fmt.Errorf("retention deletion manifest changed or unavailable")
		}
	}
	anchor := generations[strings.TrimSuffix(w.manifestName, ".manifest.json")]
	fresh, err := w.inspectRetentionGeneration(ctx, inventory[anchor.manifestName], retentionManifestName.FindStringSubmatch(anchor.manifestName)[1], inventory, budget)
	if err != nil || fresh.rawHash != anchor.rawHash {
		return 0, plan, fmt.Errorf("fresh recovery anchor changed before retention deletion")
	}
	// A newly verified local run is never deleted by its own retention pass.
	filtered := plan.Delete[:0]
	for _, id := range plan.Delete {
		if id == anchor.candidate.ID {
			plan.Keep = append(plan.Keep, id)
		} else {
			filtered = append(filtered, id)
		}
	}
	plan.Delete = filtered
	deleted := 0
	for _, id := range plan.Delete {
		for _, name := range generations[id].objects {
			if e = ctx.Err(); e != nil {
				return deleted, plan, e
			}
			if e = w.Store.Delete(ctx, name); e != nil {
				return deleted, plan, fmt.Errorf("delete %s: %w", name, e)
			}
			deleted++
		}
	}
	return deleted, plan, nil
}

func completeLocalArtifacts(stamp string, artifacts []LocalArtifact) error {
	expected := map[string]string{"urmail-" + stamp + ".dump": KindPgDump, "urmail-config-" + stamp + ".tar.gz": KindConfigTar, "urmail-mail-" + stamp + ".tar.gz": KindMailTar}
	if len(artifacts) != len(expected) {
		return fmt.Errorf("complete backup requires database, configuration and mail artifacts")
	}
	for _, artifact := range artifacts {
		kind, ok := expected[artifact.Name]
		if !ok || artifact.Kind != kind || artifact.Size <= 0 || !retentionHash.MatchString(artifact.SHA256) {
			return fmt.Errorf("complete backup artifact identity/integrity invalid")
		}
		delete(expected, artifact.Name)
	}
	return nil
}

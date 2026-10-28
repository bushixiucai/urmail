// Package marketingreport builds bounded, recomputable snapshots from durable
// facts. SMTP acceptance and provider feedback never imply delivery or opens.
package marketingreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxFacts bounds complete report snapshots; excess is rejected.
const MaxFacts = 10000

// MaxCSVBytes bounds the complete CSV response.
const MaxCSVBytes = 4 << 20

// ErrBudget indicates a report cannot be returned completely within its limits.
var ErrBudget = errors.New("marketing report budget exceeded")

// Fact is one current recipient, enrollment, instance or execution outcome.
type Fact struct {
	RecordKind string    `json:"record_kind"`
	RecordID   string    `json:"record_id"`
	Version    int32     `json:"version"`
	InstanceID int64     `json:"instance_id"`
	Email      string    `json:"email"`
	State      string    `json:"state"`
	ResultCode string    `json:"result_code"`
	OccurredAt time.Time `json:"occurred_at"`
	Weight     int64     `json:"weight"`
	Suppressed bool      `json:"suppressed"`
	Uncertain  bool      `json:"uncertain"`
}

// Scope binds a snapshot to one tenant resource and enrollment window.
type Scope struct {
	Kind       string    `json:"kind"`
	ResourceID int64     `json:"resource_id"`
	TenantID   int64     `json:"tenant_id"`
	From       time.Time `json:"from"`
	Until      time.Time `json:"until"`
}

// Snapshot contains the sorted complete facts and their reproducible digest.
type Snapshot struct {
	Scope  Scope            `json:"scope"`
	Name   string           `json:"name"`
	Rows   []Fact           `json:"rows"`
	Counts map[string]int64 `json:"counts"`
	Digest string           `json:"digest"`
}

// Build derives counters and a stable scope-bound digest from current facts.
func Build(scope Scope, name string, rows []Fact) (Snapshot, error) {
	if len(rows) > MaxFacts {
		return Snapshot{}, ErrBudget
	}
	scope.From = scope.From.UTC()
	scope.Until = scope.Until.UTC()
	facts := append([]Fact{}, rows...)
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].RecordKind != facts[j].RecordKind {
			return facts[i].RecordKind < facts[j].RecordKind
		}
		return facts[i].RecordID < facts[j].RecordID
	})
	counts := map[string]int64{}
	for i := range facts {
		f := &facts[i]
		f.OccurredAt = f.OccurredAt.UTC()
		counts[f.RecordKind+"/"+f.State]++
		if f.RecordKind == "entry" {
			counts["entry/recipients"] += f.Weight
		}
		if f.Uncertain {
			counts[f.RecordKind+"/unknown_outcome"]++
		}
		if f.Suppressed {
			counts[f.RecordKind+"/suppressed_current"]++
		}
	}
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		Scope   Scope  `json:"scope"`
		Name    string `json:"name"`
		Rows    []Fact `json:"rows"`
	}{1, scope, name, facts})
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(raw)
	return Snapshot{Scope: scope, Name: name, Rows: facts, Counts: counts, Digest: hex.EncodeToString(sum[:])}, nil
}

func spreadsheetSafe(value string) string {
	first := strings.TrimLeft(value, " \t\r\n\v\f")
	if len(first) > 0 && strings.ContainsRune("=+-@", rune(first[0])) || len(value) > 0 && strings.ContainsRune("\t\r\n", rune(value[0])) {
		return "'" + value
	}
	return value
}

// CSV includes one metadata row and every fact. It rejects oversized snapshots
// rather than returning a plausible partial export. Digest is of raw facts;
// spreadsheet apostrophes are a presentation-only safety transformation.
func CSV(snapshot Snapshot, observed time.Time) ([]byte, error) {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	header := []string{"schema_version", "report_kind", "resource_id", "resource_name", "from", "until", "observed_at", "snapshot_digest", "record_kind", "record_id", "version", "instance_id", "email", "status", "result_code", "occurred_at", "weight", "suppressed_current", "unknown_outcome"}
	if err := w.Write(header); err != nil {
		return nil, err
	}
	prefix := []string{"1", snapshot.Scope.Kind, strconv.FormatInt(snapshot.Scope.ResourceID, 10), snapshot.Name, snapshot.Scope.From.Format(time.RFC3339Nano), snapshot.Scope.Until.Format(time.RFC3339Nano), observed.UTC().Format(time.RFC3339Nano), snapshot.Digest}
	write := func(values []string) error {
		row := append(append([]string{}, prefix...), values...)
		for i := range row {
			row[i] = spreadsheetSafe(row[i])
		}
		if err := w.Write(row); err != nil {
			return err
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		if out.Len() > MaxCSVBytes {
			return ErrBudget
		}
		return nil
	}
	counts, err := json.Marshal(snapshot.Counts)
	if err != nil {
		return nil, err
	}
	if err = write([]string{"metadata", "0", "0", "0", "", "snapshot", string(counts), observed.UTC().Format(time.RFC3339Nano), strconv.Itoa(len(snapshot.Rows)), "false", "false"}); err != nil {
		return nil, err
	}
	for _, f := range snapshot.Rows {
		if err = write([]string{f.RecordKind, f.RecordID, strconv.FormatInt(int64(f.Version), 10), strconv.FormatInt(f.InstanceID, 10), f.Email, f.State, f.ResultCode, f.OccurredAt.Format(time.RFC3339Nano), strconv.FormatInt(f.Weight, 10), strconv.FormatBool(f.Suppressed), strconv.FormatBool(f.Uncertain)}); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

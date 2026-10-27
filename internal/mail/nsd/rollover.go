package nsd

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrInvalidRollover is returned when a rollover request violates the key
// lifecycle state machine (e.g. retiring a key that is not signing, or
// double-signing with a key that is still only generated).
var ErrInvalidRollover = errors.New("invalid DNSSEC rollover request")

// allowedTransitions is the linear key lifecycle:
//
//	generated -> published -> signing -> retiring -> removed
//
// Keys may also stay in place (no-op transitions are allowed). Any backward
// or skipping transition is rejected: skipping "published" means resolvers
// never saw the new DNSKEY before signatures depend on it, which breaks
// validation for cached resolvers.
var allowedTransitions = map[DNSSECKeyState][]DNSSECKeyState{
	KeyStateGenerated: {KeyStatePublished},
	KeyStatePublished: {KeyStateSigning},
	KeyStateSigning:   {KeyStateRetiring},
	KeyStateRetiring:  {KeyStateRemoved},
	KeyStateRemoved:   {},
}

// TransitionKeyState validates and returns the next lifecycle state for a
// key. It is the single gate used by the rollover planner; invalid
// transitions are rejected with ErrInvalidRollover.
func TransitionKeyState(from, to DNSSECKeyState) (DNSSECKeyState, error) {
	if from == to {
		return from, nil
	}
	for _, next := range allowedTransitions[from] {
		if next == to {
			return to, nil
		}
	}
	return from, fmt.Errorf("%w: cannot transition key from %s to %s", ErrInvalidRollover, from, to)
}

// RolloverStepKind names one ordered operation in a rollover plan.
type RolloverStepKind string

// Rollover step kinds, ordered as the planner emits them for a KSK.
const (
	StepPublishNewDNSKEY RolloverStepKind = "publish_new_dnskey"
	StepWaitPropagation  RolloverStepKind = "wait_ttl"
	StepSignWithNew      RolloverStepKind = "sign_with_new"
	StepRetireOld        RolloverStepKind = "retire_old"
	StepRemoveOld        RolloverStepKind = "remove_old"
	StepPublishDS        RolloverStepKind = "publish_ds"
	StepWithdrawOldDS    RolloverStepKind = "withdraw_old_ds"
)

// RolloverStep is one pure-data operation: the CLI renders it, the operator
// executes it. No step executes anything itself.
type RolloverStep struct {
	Order int              `json:"order"`
	Kind  RolloverStepKind `json:"kind"`
	Title string           `json:"title"`
	// Detail is the operator instruction, including the exact verification
	// command to run by hand.
	Detail string `json:"detail"`
	// WaitSeconds is the deliberate propagation wait after this step.
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// RolloverPlan is the ordered, idempotent step list for one key rollover.
type RolloverPlan struct {
	Zone    string         `json:"zone"`
	OldKey  DNSSECKey      `json:"old_key"`
	NewKey  DNSSECKey      `json:"new_key"`
	WaitTTL int            `json:"wait_ttl"`
	Steps   []RolloverStep `json:"steps"`
}

// PlanKeyRollover produces the ordered pre-publication rollover steps for one
// zone: publish the new DNSKEY, wait for it to propagate (DNSKEY TTL), sign
// with the new key while the old one still publishes signatures, then retire
// and remove the old key. For a KSK the plan additionally moves the DS record
// at the parent. The plan is pure data; execution stays with the operator
// runbook (RenderRolloverRunbook).
func PlanKeyRollover(zone string, oldKey, newKey DNSSECKey, waitTTL int) (RolloverPlan, error) {
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if zone == "" {
		return RolloverPlan{}, fmt.Errorf("%w: zone name is required", ErrInvalidRollover)
	}
	if waitTTL <= 0 {
		waitTTL = DefaultRolloverWaitTTL
	}
	// Old key must be actively signing; a retired/removed key cannot be
	// rolled over again, and a generated key was never in DNS.
	switch oldKey.State {
	case KeyStateSigning, KeyStateRetiring:
	case KeyStateGenerated, KeyStatePublished, KeyStateRemoved:
		return RolloverPlan{}, fmt.Errorf("%w: old key tag %d is in state %s, want signing (or retiring to finish a rollover)", ErrInvalidRollover, oldKey.KeyTag, oldKey.State)
	default:
		return RolloverPlan{}, fmt.Errorf("%w: old key tag %d has unknown state %q", ErrInvalidRollover, oldKey.KeyTag, oldKey.State)
	}
	// New key must not yet be trusted for signatures: freshly generated, or
	// already published but not yet signing.
	switch newKey.State {
	case KeyStateGenerated, KeyStatePublished:
	case KeyStateSigning, KeyStateRetiring, KeyStateRemoved:
		return RolloverPlan{}, fmt.Errorf("%w: new key tag %d is in state %s, want generated or published", ErrInvalidRollover, newKey.KeyTag, newKey.State)
	default:
		return RolloverPlan{}, fmt.Errorf("%w: new key tag %d has unknown state %q", ErrInvalidRollover, newKey.KeyTag, newKey.State)
	}
	if oldKey.KeyTag == newKey.KeyTag {
		return RolloverPlan{}, fmt.Errorf("%w: old and new key share key tag %d", ErrInvalidRollover, oldKey.KeyTag)
	}

	plan := RolloverPlan{Zone: zone, OldKey: oldKey, NewKey: newKey, WaitTTL: waitTTL}
	step := func(kind RolloverStepKind, title, detail string, wait int) {
		plan.Steps = append(plan.Steps, RolloverStep{Order: len(plan.Steps) + 1, Kind: kind, Title: title, Detail: detail, WaitSeconds: wait})
	}

	step(StepPublishNewDNSKEY,
		"Publish the new DNSKEY",
		fmt.Sprintf("Move the new key (tag %d) to the published state and reload so the reconcile loop renders it into the zone DNSKEY RRset. Verify with: dig DNSKEY %s +short | wc -l", newKey.KeyTag, zone),
		0)
	step(StepWaitPropagation,
		"Wait for DNSKEY propagation",
		fmt.Sprintf("Wait at least the DNSKEY TTL (%d seconds) so every resolver that cached the RRset sees the new key before any signature depends on it.", waitTTL),
		waitTTL)
	if newKey.Flags == FlagKSK && newKey.DSRecord != "" {
		step(StepPublishDS,
			"Publish the new DS at the parent",
			fmt.Sprintf("Give the new DS record to the parent zone / registrar: %s IN DS %s", zone, newKey.DSRecord),
			waitTTL)
	}
	step(StepSignWithNew,
		"Sign with the new key (double-sign window)",
		fmt.Sprintf("Transition the new key (tag %d) to the signing state so the zone carries signatures from both keys, then re-sign the zone. Keep double-signing for at least one signature refresh cycle.", newKey.KeyTag),
		0)
	if oldKey.Flags == FlagKSK {
		step(StepWithdrawOldDS,
			"Withdraw the old DS at the parent",
			fmt.Sprintf("Remove the old DS for key tag %d from the parent zone and wait the TTL again before retiring the key.", oldKey.KeyTag),
			waitTTL)
	}
	step(StepRetireOld,
		"Retire the old key",
		fmt.Sprintf("Transition the old key (tag %d) to the retiring state and re-sign; the old key stays in the DNSKEY RRset but stops producing signatures.", oldKey.KeyTag),
		waitTTL)
	step(StepRemoveOld,
		"Remove the old key",
		fmt.Sprintf("After the retirement wait, transition the old key (tag %d) to the removed state, reload, and archive or delete its private key from the key directory. Verify validation with: dig +dnssec %s SOA", oldKey.KeyTag, zone),
		0)

	return plan, nil
}

// RenderRolloverRunbook renders the plan as a CLI/operator runbook (plain
// markdown). Pure function of the plan.
func RenderRolloverRunbook(plan RolloverPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# DNSSEC key rollover runbook: %s\n\n", plan.Zone)
	fmt.Fprintf(&b, "Old key: tag %d (flags %d, algorithm %d, state %s)\n", plan.OldKey.KeyTag, plan.OldKey.Flags, plan.OldKey.Algorithm, plan.OldKey.State)
	fmt.Fprintf(&b, "New key: tag %d (flags %d, algorithm %d, state %s)\n\n", plan.NewKey.KeyTag, plan.NewKey.Flags, plan.NewKey.Algorithm, plan.NewKey.State)
	fmt.Fprintf(&b, "Execute the steps in order. Do not skip the TTL waits: cached resolvers\nwill fail validation otherwise.\n\n")
	for _, s := range plan.SortSteps() {
		fmt.Fprintf(&b, "## %d. %s\n\n%s\n", s.Order, s.Title, s.Detail)
		if s.WaitSeconds > 0 {
			fmt.Fprintf(&b, "\nWait: %d seconds.\n", s.WaitSeconds)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// SortSteps returns the plan steps ordered by Order (defensive copy).
func (p RolloverPlan) SortSteps() []RolloverStep {
	out := make([]RolloverStep, len(p.Steps))
	copy(out, p.Steps)
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

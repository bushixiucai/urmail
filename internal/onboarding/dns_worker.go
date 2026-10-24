package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/store"
)

var errFence = errors.New("DNS_LEASE_OR_VERSION_CHANGED")

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
func dnsAudit(ctx context.Context, q *store.Queries, tenant int64, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, Action: action, TargetType: text("dns_plan"), TargetID: text(target), Detail: raw})
	return err
}

// DNSOnce claims one fenced, explicitly confirmed plan. Unknown upstream
// results become uncertain and are never replayed automatically.
func (s *Service) DNSOnce(ctx context.Context, owner string) (bool, error) {
	if config.RestoreQuarantined() {
		return false, errors.New("RESTORE_QUARANTINED")
	}
	q := store.New(s.Pool.Pool)
	if _, err := q.PlatformExpireDNSClaims(ctx); err != nil {
		return false, err
	}
	p, err := q.PlatformClaimDNSPlan(ctx, text(owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	started := time.Now()
	err = s.applyDNS(ctx, p)
	observeWork("dns", p.Kind, started, err)
	if err != nil {
		_, holdErr := q.PlatformAdvanceDNSPlan(ctx, store.PlatformAdvanceDNSPlanParams{ID: p.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner, Status: "uncertain", LastErrorCode: text("DNS_RESULT_REQUIRES_REVIEW")})
		if holdErr != nil {
			return true, holdErr
		}
	}
	if err == nil && s.AfterChange != nil {
		s.AfterChange()
	}
	return true, err
}
func (s *Service) dnsFence(ctx context.Context, q *store.Queries, p store.DnsChangePlans) error {
	if _, err := q.PlatformFenceDNSPlan(ctx, store.PlatformFenceDNSPlanParams{ID: p.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner}); err != nil {
		return errFence
	}
	if p.Kind == "provider" {
		cred, err := q.PlatformGetDNSCredential(ctx, store.PlatformGetDNSCredentialParams{TenantID: p.TenantID, DomainID: p.DomainID})
		if err != nil || cred.ID != p.CredentialID.Int64 || cred.Version != p.CredentialVersion.Int64 {
			return errFence
		}
	}
	return nil
}
func (s *Service) applyDNS(ctx context.Context, p store.DnsChangePlans) error {
	q := store.New(s.Pool.Pool)
	d, err := q.PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: p.TenantID, ID: p.DomainID})
	if err != nil {
		return err
	}
	digest, desired, err := DomainDigest(d, s.Hostname)
	if err != nil || digest != p.DomainDigest {
		return errors.New("DNS_DOMAIN_CHANGED")
	}
	var before []dnsprovider.RemoteRecord
	var changes Changes
	if json.Unmarshal(p.BeforeRecords, &before) != nil || json.Unmarshal(p.Changes, &changes) != nil || len(changes.Conflicts) > 0 {
		return errors.New("DNS_PLAN_INVALID")
	}
	rebuilt, err := BuildChanges(d.Fqdn, before, desired)
	if err != nil || Digest(rebuilt) != Digest(changes) {
		return errors.New("DNS_PLAN_CHANGED")
	}
	if p.Kind != "provider" {
		return s.applyLocalDNS(ctx, p, before, changes)
	}
	cred, manager, err := s.credentialManager(ctx, p.TenantID, p.DomainID)
	if err != nil {
		return err
	}
	if cred.Version != p.CredentialVersion.Int64 {
		return errFence
	}
	zone, err := manager.Probe(ctx, cred.ZoneName)
	if err != nil || zone.ID != cred.ZoneID {
		return errors.New("DNS_ZONE_CHANGED")
	}
	current, err := manager.List(ctx, zone)
	if err != nil {
		return err
	}
	if p.Status == "rollback_pending" {
		return s.rollbackProviderDNS(ctx, p, manager, zone, current)
	}
	if SnapshotDigest(current) != p.SourceDigest {
		return errors.New("DNS_SOURCE_CHANGED")
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tq := store.New(tx)
	if err = s.dnsFence(ctx, tq, p); err != nil {
		return err
	}
	for i, a := range changes.Actions {
		raw, _ := json.Marshal(a.After)
		var old []byte
		if a.Before != nil {
			old, _ = json.Marshal(a.Before)
		}
		if err = tq.PlatformCreateDNSAction(ctx, store.PlatformCreateDNSActionParams{TenantID: p.TenantID, PlanID: p.ID, Ordinal: int32(i), Kind: a.Kind, BeforeRecord: old, AfterRecord: raw}); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	actions, err := q.PlatformListDNSActions(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.Status != "pending" {
			return errors.New("DNS_ACTION_REQUIRES_REVIEW")
		}
		var after dnsprovider.Record
		var old *dnsprovider.RemoteRecord
		if json.Unmarshal(a.AfterRecord, &after) != nil {
			return errors.New("DNS_PLAN_INVALID")
		}
		if a.Kind == "update" {
			old = &dnsprovider.RemoteRecord{}
			if json.Unmarshal(a.BeforeRecord, old) != nil {
				return errors.New("DNS_PLAN_INVALID")
			}
		}
		live, e := manager.List(ctx, zone)
		if e != nil {
			return e
		}
		if SnapshotDigest(live) != SnapshotDigest(current) {
			return errors.New("DNS_SOURCE_CHANGED")
		}
		if err = s.submitDNS(ctx, p, a, false); err != nil {
			return err
		}
		var result dnsprovider.RemoteRecord
		if old == nil {
			result, err = manager.Create(ctx, zone, after)
		} else {
			result, err = manager.Update(ctx, zone, *old, after)
		}
		if err != nil {
			_ = s.finishDNSAction(ctx, p, a, "uncertain", dnsprovider.RemoteRecord{}, false)
			return errors.New("DNS_RESULT_UNKNOWN")
		}
		observed, observedResult, observeErr := observeDNSWrite(ctx, manager, zone, current, result, false)
		if observeErr != nil {
			_ = s.finishDNSAction(ctx, p, a, "uncertain", result, false)
			return observeErr
		}
		if err = s.finishDNSAction(ctx, p, a, "applied", observedResult, old == nil); err != nil {
			return err
		}
		current = observed
	}
	return s.completeDNS(ctx, p, "completed")
}
func (s *Service) submitDNS(ctx context.Context, p store.DnsChangePlans, a store.DnsRecordActions, rollback bool) error {
	if config.RestoreQuarantined() {
		return errors.New("RESTORE_QUARANTINED")
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = s.dnsFence(ctx, q, p); err != nil {
		return err
	}
	n, err := q.PlatformRenewDNSLease(ctx, store.PlatformRenewDNSLeaseParams{ID: p.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner})
	if err != nil || n != 1 {
		return errFence
	}
	if rollback {
		n, err = q.PlatformSubmitDNSRollback(ctx, store.PlatformSubmitDNSRollbackParams{ID: a.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner})
	} else {
		n, err = q.PlatformSubmitDNSAction(ctx, store.PlatformSubmitDNSActionParams{ID: a.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner})
	}
	if err != nil || n != 1 {
		return errFence
	}
	if err = dnsAudit(ctx, q, p.TenantID, "dns.record_submitting", strconv.FormatInt(a.ID, 10), map[string]any{"plan_id": p.ID, "ordinal": a.Ordinal, "rollback": rollback}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) finishDNSAction(ctx context.Context, p store.DnsChangePlans, a store.DnsRecordActions, state string, result dnsprovider.RemoteRecord, owned bool) error {
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	observed, _ := json.Marshal(result)
	n, err := q.PlatformCompleteDNSAction(ctx, store.PlatformCompleteDNSActionParams{ID: a.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner, Status: state, ProviderRecordID: text(result.ID), Owned: owned, ObservedRecord: observed, LastErrorCode: text(map[bool]string{true: "DNS_RESULT_UNKNOWN"}[state == "uncertain"])})
	if err != nil || n != 1 {
		return errFence
	}
	if err = dnsAudit(ctx, q, p.TenantID, "dns.record_"+state, strconv.FormatInt(a.ID, 10), map[string]any{"plan_id": p.ID, "upstream_id": result.ID, "owned": owned}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) completeDNS(ctx context.Context, p store.DnsChangePlans, state string) error {
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = s.dnsFence(ctx, q, p); err != nil {
		return err
	}
	n, err := q.PlatformAdvanceDNSPlan(ctx, store.PlatformAdvanceDNSPlanParams{ID: p.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner, Status: state})
	if err != nil || n != 1 {
		return errFence
	}
	if err = dnsAudit(ctx, q, p.TenantID, "dns.plan_"+state, p.ID.String(), map[string]any{"kind": p.Kind}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) applyLocalDNS(ctx context.Context, p store.DnsChangePlans, before []dnsprovider.RemoteRecord, changes Changes) error {
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = s.dnsFence(ctx, q, p); err != nil {
		return err
	}
	if p.Kind == "managed" {
		var records []nsd.Record
		if p.Status == "rollback_pending" {
			current, e := q.PlatformCurrentManagedDNS(ctx, store.PlatformCurrentManagedDNSParams{TenantID: p.TenantID, DomainID: p.DomainID})
			if e != nil || current.PlanID != p.ID {
				return errors.New("DNS_MANAGED_VERSION_CHANGED")
			}
			records, err = ManagedRecords((func() string {
				d, e := q.PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: p.TenantID, ID: p.DomainID})
				if e != nil {
					return ""
				}
				return d.Fqdn
			})(), before, Changes{})
		} else {
			d, e := q.PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: p.TenantID, ID: p.DomainID})
			if e != nil {
				return e
			}
			records, err = ManagedRecords(d.Fqdn, before, changes)
		}
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(records)
		if err = q.PlatformApplyManagedDNS(ctx, store.PlatformApplyManagedDNSParams{TenantID: p.TenantID, DomainID: p.DomainID, Records: raw, PlanID: p.ID}); err != nil {
			return err
		}
		if err = q.PlatformSetExplicitDNS(ctx, store.PlatformSetExplicitDNSParams{TenantID: p.TenantID, ID: p.DomainID}); err != nil {
			return err
		}
	}
	state := "completed"
	if p.Status == "rollback_pending" {
		state = "rolled_back"
	}
	n, err := q.PlatformAdvanceDNSPlan(ctx, store.PlatformAdvanceDNSPlanParams{ID: p.ID, Fence: p.Fence, LeaseOwner: p.LeaseOwner, Status: state})
	if err != nil || n != 1 {
		return errFence
	}
	if err = dnsAudit(ctx, q, p.TenantID, "dns.plan_"+state, p.ID.String(), map[string]any{"kind": p.Kind}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) rollbackProviderDNS(ctx context.Context, p store.DnsChangePlans, manager dnsprovider.RecordManager, zone dnsprovider.Zone, current []dnsprovider.RemoteRecord) error {
	actions, err := store.New(s.Pool.Pool).PlatformListDNSActions(ctx, p.ID)
	if err != nil {
		return err
	}
	for i := len(actions) - 1; i >= 0; i-- {
		a := actions[i]
		if a.Status == "rolled_back" {
			continue
		}
		if a.Status != "applied" || (!a.Owned && a.Kind != "update") || !a.ProviderRecordID.Valid {
			return errors.New("DNS_ROLLBACK_OWNERSHIP_UNPROVEN")
		}
		var after dnsprovider.Record
		if json.Unmarshal(a.AfterRecord, &after) != nil {
			return errors.New("DNS_PLAN_INVALID")
		}
		var matched *dnsprovider.RemoteRecord
		for _, r := range current {
			if r.ID == a.ProviderRecordID.String && EqualValue(r.Record, after) {
				copyRecord := r
				matched = &copyRecord
				break
			}
		}
		var observedRecord dnsprovider.RemoteRecord
		if matched == nil || json.Unmarshal(a.ObservedRecord, &observedRecord) != nil || Digest(observedRecord) != Digest(*matched) {
			return errors.New("DNS_ROLLBACK_SOURCE_CHANGED")
		}
		if err = s.submitDNS(ctx, p, a, true); err != nil {
			return err
		}
		result := *matched
		if a.Kind == "create" {
			err = manager.Delete(ctx, zone, *matched)
		} else {
			var before dnsprovider.RemoteRecord
			if json.Unmarshal(a.BeforeRecord, &before) != nil {
				return errors.New("DNS_PLAN_INVALID")
			}
			result, err = manager.Update(ctx, zone, *matched, before.Record)
		}
		if err != nil {
			_ = s.finishDNSAction(ctx, p, a, "uncertain", dnsprovider.RemoteRecord{}, false)
			return errors.New("DNS_ROLLBACK_RESULT_UNKNOWN")
		}
		observed, observedResult, observeErr := observeDNSWrite(ctx, manager, zone, current, result, a.Kind == "create")
		if observeErr != nil {
			_ = s.finishDNSAction(ctx, p, a, "uncertain", result, false)
			return observeErr
		}
		if err = s.finishDNSAction(ctx, p, a, "rolled_back", observedResult, a.Owned); err != nil {
			return err
		}
		current = observed
	}
	return s.completeDNS(ctx, p, "rolled_back")
}

// ObservePlan only reads DNS. It cannot prove uncertain creation ownership;
// explicit acceptance preserves an unowned journal so later rollback is safe.
func (s *Service) ObservePlan(ctx context.Context, p store.DnsChangePlans) error {
	if p.Kind != "provider" {
		return errors.New("DNS_REVIEW_REQUIRES_MANAGED_INSPECTION")
	}
	cred, manager, err := s.credentialManager(ctx, p.TenantID, p.DomainID)
	if err != nil {
		return err
	}
	zone, err := manager.Probe(ctx, cred.ZoneName)
	if err != nil || zone.ID != cred.ZoneID {
		return errors.New("DNS_ZONE_CHANGED")
	}
	records, err := manager.List(ctx, zone)
	if err != nil {
		return err
	}
	var changes Changes
	if json.Unmarshal(p.Changes, &changes) != nil {
		return errors.New("DNS_PLAN_INVALID")
	}
	for _, a := range changes.Actions {
		found := 0
		for _, r := range records {
			if EqualValue(r.Record, a.After) {
				found++
			}
		}
		if found != 1 {
			return fmt.Errorf("DNS_OBSERVATION_INCOMPLETE")
		}
	}
	return nil
}

// observeDNSWrite tolerates documented provider indexing delay without issuing
// another write. Any unrelated record change stops the plan for review.
func observeDNSWrite(ctx context.Context, manager dnsprovider.RecordManager, zone dnsprovider.Zone, before []dnsprovider.RemoteRecord, result dnsprovider.RemoteRecord, removed bool) ([]dnsprovider.RemoteRecord, dnsprovider.RemoteRecord, error) {
	pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	without := func(records []dnsprovider.RemoteRecord) []dnsprovider.RemoteRecord {
		out := make([]dnsprovider.RemoteRecord, 0, len(records))
		for _, r := range records {
			if r.ID != result.ID {
				out = append(out, r)
			}
		}
		return out
	}
	baseline := SnapshotDigest(without(before))
	for {
		records, err := manager.List(pollCtx, zone)
		if err == nil {
			var found *dnsprovider.RemoteRecord
			for _, r := range records {
				if r.ID == result.ID {
					observed := r
					found = &observed
					break
				}
			}
			if SnapshotDigest(without(records)) != baseline {
				return nil, result, errors.New("DNS_SOURCE_CHANGED")
			}
			if removed && found == nil {
				return records, result, nil
			}
			if !removed && found != nil && EqualValue(found.Record, result.Record) {
				return records, *found, nil
			}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return nil, result, errors.New("DNS_WRITE_OBSERVATION_UNCERTAIN")
		case <-timer.C:
		}
	}
}

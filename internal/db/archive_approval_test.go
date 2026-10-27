package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestArchiveApprovalRejectsSelfAndTargetReuse(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	exec := func(sql string, args ...any) {
		if _, err = tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('apr','Apr') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','apr-a','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver)
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at) VALUES($1,$2,$3,'export',$4,$5) RETURNING id", tenant, investigation, requester, digest, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = tx.QueryRow(ctx, "SELECT urmail_decide_archive_approval($1,$2,$3,'approved',$4)", tenant, request, requester, digest).Scan(&result); err != nil || result != "self_approval_denied" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_decide_archive_approval($1,$2,$3,'approved',$4)", tenant, request, approver, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff").Scan(&result); err != nil || result != "target_mismatch" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_decide_archive_approval($1,$2,$3,'approved',$4)", tenant, request, approver, digest).Scan(&result); err != nil || result != "ok" {
		t.Fatalf("%s %v", result, err)
	}
}

func TestEvidenceManifestRequiresApprovedTarget(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('ev','Ev') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'E','ev-e','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('ev-requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('ev-approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver); err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at) VALUES($1,$2,$3,'export',$4,$5) RETURNING id", tenant, investigation, requester, digest, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = tx.QueryRow(ctx, "SELECT urmail_store_evidence_manifest($1,$2,$3,$4,$5)", tenant, request, approver, digest, manifest).Scan(&result); err != nil || result != "closed" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_decide_archive_approval($1,$2,$3,'approved',$4)", tenant, request, approver, digest).Scan(&result); err != nil || result != "ok" {
		t.Fatal(result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_store_evidence_manifest($1,$2,$3,$4,$5)", tenant, request, approver, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", manifest).Scan(&result); err != nil || result != "target_mismatch" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_store_evidence_manifest($1,$2,$3,$4,$5)", tenant, request, approver, digest, manifest).Scan(&result); err != nil || result != "ok" {
		t.Fatalf("%s %v", result, err)
	}
}

func TestRemovedMemberCannotSeeEvidence(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('vis','Vis') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'V','vis-v','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('vis-requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('vis-approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver); err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,evidence_digest,expires_at) VALUES($1,$2,$3,'export',$4,'approved',$5,$6,$7) RETURNING id", tenant, investigation, requester, digest, approver, manifest, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var visible bool
	if err = tx.QueryRow(ctx, "SELECT urmail_evidence_manifest_visible($1,$2,$3,$4)", tenant, request, approver, manifest).Scan(&visible); err != nil || !visible {
		t.Fatal(visible, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE archive_investigation_members SET removed_at=now() WHERE user_id=$1", approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_evidence_manifest_visible($1,$2,$3,$4)", tenant, request, approver, manifest).Scan(&visible); err != nil || visible {
		t.Fatal(visible, err)
	}
}

func TestEvidenceQueryHidesRemovedMember(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('query','Query') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Q','query-q','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('query-requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('query-approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver); err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,evidence_digest,expires_at) VALUES($1,$2,$3,'export',$4,'approved',$5,$6,$7) RETURNING id", tenant, investigation, requester, digest, approver, manifest, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	visible, err := q.PlatformEvidenceVisible(ctx, store.PlatformEvidenceVisibleParams{PTenant: tenant, PRequest: request, PActor: approver, PDigest: manifest})
	if err != nil || !visible {
		t.Fatal(visible, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE archive_investigation_members SET removed_at=now() WHERE user_id=$1", approver); err != nil {
		t.Fatal(err)
	}
	visible, err = q.PlatformEvidenceVisible(ctx, store.PlatformEvidenceVisibleParams{PTenant: tenant, PRequest: request, PActor: approver, PDigest: manifest})
	if err != nil || visible {
		t.Fatal(visible, err)
	}
}

func TestEvidenceBodyRejectsDigestMismatch(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('body','Body') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'B','body-b','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('body-requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('body-approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver); err != nil {
		t.Fatal(err)
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wrong := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,evidence_digest,expires_at) VALUES($1,$2,$3,'export',$4,'approved',$5,$6,$7) RETURNING id", tenant, investigation, requester, digest, approver, wrong, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = tx.QueryRow(ctx, "SELECT urmail_store_evidence_body($1,$2,$3,$4,$5::bytea)", tenant, request, approver, wrong, "manifest").Scan(&result); err != nil || result != "digest_mismatch" {
		t.Fatalf("%s %v", result, err)
	}
	var stored bool
	if err = tx.QueryRow(ctx, "SELECT evidence_body IS NOT NULL FROM archive_approval_requests WHERE id=$1", request).Scan(&stored); err != nil || stored {
		t.Fatal(stored, err)
	}
}

func TestHiddenEvidenceBodyIsNotReturned(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, requester, approver, investigation, request int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('readbody','Read') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'R','read-r','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('read-requester@example.test','x') RETURNING id").Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('read-approver@example.test','x') RETURNING id").Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Case','purpose',$2,$3) RETURNING id", tenant, requester, time.Now().Add(time.Hour)).Scan(&investigation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id) VALUES($1,$2,$3),($1,$2,$4)", investigation, tenant, requester, approver); err != nil {
		t.Fatal(err)
	}
	body := []byte("manifest")
	var digest string
	if err = tx.QueryRow(ctx, "SELECT encode(sha256($1::bytea),'hex')", body).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	source := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err = tx.QueryRow(ctx, "INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,evidence_digest,evidence_body,expires_at) VALUES($1,$2,$3,'export',$4,'approved',$5,$6,$7,$8) RETURNING id", tenant, investigation, requester, source, approver, digest, body, time.Now().Add(time.Hour)).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err = tx.QueryRow(ctx, "SELECT urmail_read_evidence_body($1,$2,$3,$4)", tenant, request, approver, digest).Scan(&got); err != nil || string(got) != "manifest" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE archive_investigation_members SET removed_at=now() WHERE user_id=$1", approver); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_read_evidence_body($1,$2,$3,$4)", tenant, request, approver, digest).Scan(&got); err != nil || got != nil {
		t.Fatalf("%q %v", got, err)
	}
}

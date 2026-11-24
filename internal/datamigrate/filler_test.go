package datamigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pgxpool "github.com/jackc/pgx/v5/pgxpool"
)

// This file is test infrastructure for the S5 pipeline: a catalog-driven
// synthetic sample generator. It reads NOT NULL/default/serial metadata from
// information_schema, value domains from CHECK constraint definitions, and
// fills every type-map table with FK-consistent, tenant-aligned rows.

type colMeta struct {
	nullable   bool
	def        sql.NullString
	serial     bool
	hasDefault bool
}

type checkDomains map[string]map[string][]string

var (
	checkColRe   = regexp.MustCompile(`"?(\w+)"?\s*(?:=|IN|~|>=|<=|<>|>|<)`)
	checkLitRe   = regexp.MustCompile(`'([^']{1,64})'`)
	nonWordStart = regexp.MustCompile(`^[0-9_]`)
)

func parseCheckDomains(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}) checkDomains {
	out := checkDomains{}
	rows, err := q.Query(ctx, `SELECT conrelid::regclass::text, pg_get_constraintdef(oid) FROM pg_constraint WHERE contype='c'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var table, def string
		if err := rows.Scan(&table, &def); err != nil {
			continue
		}
		table = strings.Trim(table, `"`)
		lower := strings.ToLower(def)
		colMatches := checkColRe.FindAllStringSubmatch(lower, -1)
		litMatches := checkLitRe.FindAllStringSubmatch(def, -1)
		if len(colMatches) == 0 || len(litMatches) == 0 {
			continue
		}
		if out[table] == nil {
			out[table] = map[string][]string{}
		}
		for _, cm := range colMatches {
			col := strings.ToLower(cm[1])
			if nonWordStart.MatchString(col) {
				continue
			}
			for _, lm := range litMatches {
				lit := lm[1]
				if len(lit) < 3 || lit == "" {
					continue
				}
				out[table][col] = appendUnique(out[table][col], lit)
			}
			// Keep domains small and deterministic.
			if len(out[table][col]) > 8 {
				out[table][col] = out[table][col][:8]
			}
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

func (d checkDomains) lit(table, col string) []string {
	if d == nil {
		return nil
	}
	return d[table][col]
}

// orderTablesForSeed re-orders the FK-topological plan so every table with a
// tenant_id column is seeded after tenants (synthetic dependency), keeping
// the real FK order otherwise.
func orderTablesForSeed(plan []TableInfo) []TableInfo {
	deps := map[string]map[string]bool{}
	for _, ti := range plan {
		deps[ti.Name] = map[string]bool{}
		for _, fk := range ti.FKs {
			for _, other := range plan {
				if other.Name == fk.RefTable {
					deps[ti.Name][fk.RefTable] = true
				}
			}
		}
		hasTenant := false
		for _, c := range ti.Columns {
			if c == "tenant_id" && ti.Name != "tenants" {
				hasTenant = true
			}
		}
		if hasTenant {
			deps[ti.Name]["tenants"] = true
		}
		// The compliance owner trigger reads user_tenant_roles for the owner
		// role; seed the roles first.
		if ti.Name == "compliance_grants" {
			deps[ti.Name]["user_tenant_roles"] = true
		}
		// Reservation addresses chain to real mailbox addresses.
		if ti.Name == "mailbox_storage_reservations" {
			deps[ti.Name]["mailboxes"] = true
		}
		// dns_zone_versions.domain references a live domain fqdn.
		if ti.Name == "dns_zone_versions" {
			deps[ti.Name]["domains"] = true
		}
		// Recall previews reference archive_entries (target-mismatch guard).
		if ti.Name == "recall_requests" {
			deps[ti.Name]["archive_entries"] = true
		}
		// Retention policies need a retention_manage grant (owner trigger).
		if ti.Name == "retention_policies" {
			deps[ti.Name]["compliance_grants"] = true
			deps[ti.Name]["users"] = true
		}
		if ti.Name == "retention_previews" {
			deps[ti.Name]["compliance_grants"] = true
		}
	}
	placed := map[string]bool{}
	var order []TableInfo
	for len(order) < len(plan) {
		progress := false
		for _, ti := range plan {
			if placed[ti.Name] {
				continue
			}
			ready := true
			for dep := range deps[ti.Name] {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				placed[ti.Name] = true
				order = append(order, ti)
				progress = true
			}
		}
		if !progress {
			// FK cycle: append the remainder in plan order (the migrator's
			// own topo already rejected real cycles; synthetic edges only).
			for _, ti := range plan {
				if !placed[ti.Name] {
					order = append(order, ti)
				}
			}
			break
		}
	}
	return order
}

type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

type pgxpoolShim = pgxpool.Pool

func seedSyntheticSample(ctx context.Context, t testingT, pool *pgxpoolShim, tm TypeMap, rowsPer int) syntheticSample {
	t.Helper()
	src := pgSource{pool: pool}
	plan, _, err := IntrospectSource(ctx, src)
	if err != nil {
		t.Fatalf("introspect source: %v", err)
	}
	ordered := orderTablesForSeed(plan)

	// One dedicated connection: the per-tenant/user session GUCs must stay
	// visible to every subsequent insert on the same session (pool
	// connections would rotate them away).
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire seeding connection: %v", err)
	}
	defer conn.Release()

	colInfo := map[string]map[string]colMeta{}
	rows, err := pool.Query(ctx, `
SELECT table_name, column_name, is_nullable, column_default, is_identity, identity_generation
FROM information_schema.columns
WHERE table_schema='public'`)
	if err != nil {
		t.Fatalf("column metadata: %v", err)
	}
	for rows.Next() {
		var table, col, nullable, identity string
		var def, idgen sql.NullString
		if err := rows.Scan(&table, &col, &nullable, &def, &identity, &idgen); err != nil {
			rows.Close()
			t.Fatalf("scan column meta: %v", err)
		}
		if colInfo[table] == nil {
			colInfo[table] = map[string]colMeta{}
		}
		colInfo[table][col] = colMeta{
			nullable:   nullable == "YES",
			def:        def,
			hasDefault: def.Valid && strings.TrimSpace(def.String) != "",
			serial: (def.Valid && strings.Contains(def.String, "nextval(")) ||
				(identity == "YES" && strings.Contains(idgen.String, "ALWAYS")),
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("column metadata rows: %v", err)
	}

	domains := parseCheckDomains(ctx, src)
	// Empty-string domains are useless for NOT NULL synthesis.
	for table := range domains {
		for col := range domains[table] {
			filtered := domains[table][col][:0]
			for _, v := range domains[table][col] {
				if strings.TrimSpace(v) != "" {
					filtered = append(filtered, v)
				}
			}
			domains[table][col] = filtered
		}
	}

	counters := map[string]int{}
	ids := map[string][]int64{}
	tenantIDs := []int64{}
	userIDs := []int64{}
	ownersByTenant := map[int64][]int64{} // primary, secondary
	grantIDByTenant := map[int64]int64{}  // secondary-owner retention_manage grant
	sample := syntheticSample{
		Tables:      map[string]int{},
		Failures:    map[string][]string{},
		SettingsKey: "syn-key-1",
	}
	hexOf := func(n int) string {
		counters["hex"]++
		digits := fmt.Sprintf("%x", counters["hex"])
		if len(digits) >= n {
			return digits[len(digits)-n:]
		}
		return strings.Repeat("0", n-len(digits)) + digits
	}
	uuidLike := func(int, string) string {
		base := hexOf(32)
		u := fmt.Sprintf("%s-%s-4%s-8%s-%s", base[:8], base[8:12], base[12:15], base[17:20], base[20:32])
		return u
	}

	// table-specific value chains (no catalog FK edges to lean on).
	overrides := map[string]func(rowIdx int, parentLookups map[string]int64, tenantID int64) (any, error){
		"restore_quarantine.reason": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return "restore", nil
		},
		"user_tenant_roles.role": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return "owner", nil
		},
		"tenants.status": func(_ int, _ map[string]int64, _ int64) (any, error) {
			// Recall/branding authority guards require an active tenant.
			return "active", nil
		},
		"notification_outbox.kind": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return "email", nil
		},
		"notification_outbox.channel": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return "email", nil
		},
		"mailbox_spam_senders.scope": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return "mailbox", nil
		},
		"notification_outbox.recipient": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return fmt.Sprintf("syn%d@example.test", rowIdx), nil
		},
		"notification_outbox.recipient_verified": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return true, nil
		},
		"mailbox_storage_reservations.address": func(rowIdx int, parentLookups map[string]int64, _ int64) (any, error) {
			if id, ok := parentLookups["mailbox_id"]; ok {
				var addr string
				if err := conn.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", id).Scan(&addr); err == nil && addr != "" {
					return addr, nil
				}
			}
			return fmt.Sprintf("syn%d@example.test", rowIdx), nil
		},
		"archive_evidence_exports.manifest_sha256": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			sum := sha256OfBytes([]byte{0xde, 0xad, 0xbe, byte(rowIdx % 256)})
			return sum, nil
		},
		"compliance_grants.granted_by": func(_ int, _ map[string]int64, tenantID int64) (any, error) {
			// The issuer is the primary owner — identical to the session GUC
			// user the issuer check compares against.
			if owners, ok := ownersByTenant[tenantID]; ok && len(owners) > 0 {
				return owners[0], nil
			}
			var id int64
			if err := pool.QueryRow(ctx,
				"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
				tenantID).Scan(&id); err != nil {
				return nil, err
			}
			return id, nil
		},
		"settings.key": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			keys := []string{"business_timezone", "locale", "theme"}
			return keys[(rowIdx-1)%len(keys)], nil
		},
		"settings.value": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			values := []string{`"Asia/Shanghai"`, `"zh-CN"`, `{"primary":"#123456"}`}
			return values[(rowIdx-1)%len(values)], nil
		},
		"dns_zone_versions.domain": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			var fqdn string
			if err := conn.QueryRow(ctx, "SELECT fqdn FROM domains ORDER BY 1 LIMIT 1 OFFSET $1", rowIdx-1).Scan(&fqdn); err == nil && fqdn != "" {
				return fqdn, nil
			}
			return "syn.example.test", nil
		},
		"brand_host_challenges.hostname": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			var host string
			if err := pool.QueryRow(ctx, "SELECT hostname FROM brand_hosts ORDER BY 1 LIMIT 1 OFFSET $1", rowIdx-1).Scan(&host); err == nil && host != "" {
				return host, nil
			}
			return "syn-brand.example.test", nil
		},
		"compliance_grants.owner_confirmed_at": func(_ int, _ map[string]int64, _ int64) (any, error) {
			return time.UnixMicro(nowMicros() - 1_000_000), nil
		},
		"compliance_grants.expires_at": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return time.UnixMicro(nowMicros() + 3_600_000_000), nil
		},
		"compliance_grants.reason": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return "synthetic independent grant", nil
		},
		"compliance_grants.scope": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			// Every synthetic tenant receives a retention_manage grant so the
			// retention policy/preview owner triggers find their authorization.
			return "retention_manage", nil
		},
		"compliance_grants.user_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			// The grantee is the secondary owner: grants require the grantee
			// to hold owner/admin and self-authorization is rejected.
			if owners, ok := ownersByTenant[tenantID]; ok && len(owners) > 1 {
				return owners[1], nil
			}
			var id int64
			if err := pool.QueryRow(ctx,
				"SELECT id FROM users WHERE id NOT IN (SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner') ORDER BY 1 LIMIT 1",
				tenantID).Scan(&id); err != nil {
				return nil, err
			}
			return id, nil
		},
		"mailbox_aliases.target": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return fmt.Sprintf("syn%d@example.test", rowIdx), nil
		},
		"recall_requests.message_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			var mid string
			if err := pool.QueryRow(ctx,
				"SELECT message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1", tenantID).Scan(&mid); err != nil {
				return nil, err
			}
			return mid, nil
		},
		"recall_requests.archive_entry_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			var id int64
			if err := pool.QueryRow(ctx,
				"SELECT id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1", tenantID).Scan(&id); err != nil {
				return nil, err
			}
			return id, nil
		},
		"recall_requests.mailbox_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			var id int64
			if err := pool.QueryRow(ctx,
				"SELECT a.mailbox_id FROM archive_entries a WHERE a.tenant_id=$1 ORDER BY a.id LIMIT 1", tenantID).Scan(&id); err != nil {
				return nil, err
			}
			return id, nil
		},
		"recall_requests.preview_snapshot": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			var entryID, mailboxID int64
			var mid, addr string
			if err := pool.QueryRow(ctx,
				"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1", tenantID).Scan(&entryID, &mailboxID, &mid); err != nil {
				return nil, err
			}
			if err := pool.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", mailboxID).Scan(&addr); err != nil {
				return nil, err
			}
			var owner int64
			_ = pool.QueryRow(ctx,
				"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1", tenantID).Scan(&owner)
			return fmt.Sprintf(`{"tenant_id":%d,"actor_id":%d,"entry_id":%d,"mailbox_id":%d,"address":%q,"message_id":%q,"copies":[]}`,
				tenantID, owner, entryID, mailboxID, addr, mid), nil
		},
		"recall_requests.preview_actor_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			var id int64
			if err := pool.QueryRow(ctx,
				"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1", tenantID).Scan(&id); err != nil {
				return nil, err
			}
			return id, nil
		},

		"retention_policies.created_by": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			if owners, ok := ownersByTenant[tenantID]; ok && len(owners) > 1 {
				return owners[1], nil
			}
			return nil, fmt.Errorf("no owners for tenant %d", tenantID)
		},
		"retention_policies.authorization_grant_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			if id, ok := grantIDByTenant[tenantID]; ok {
				return id, nil
			}
			return nil, fmt.Errorf("no retention_manage grant for tenant %d", tenantID)
		},
		"retention_previews.authorization_grant_id": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			if id, ok := grantIDByTenant[tenantID]; ok {
				return id, nil
			}
			return nil, fmt.Errorf("no retention_manage grant for tenant %d", tenantID)
		},
		"retention_previews.created_by": func(rowIdx int, _ map[string]int64, tenantID int64) (any, error) {
			if owners, ok := ownersByTenant[tenantID]; ok && len(owners) > 1 {
				return owners[1], nil
			}
			return nil, fmt.Errorf("no owners for tenant %d", tenantID)
		},
		"domains.provisioning_state": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			// The SQLite side trigger (§3.4 port) requires ready provisioning
			// before child rows may reference the domain.
			return "ready", nil
		},
		"domains.status": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return "active", nil
		},
		"domains.verified_at": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return time.UnixMicro(nowMicros() - int64(rowIdx)*1_000_000), nil
		},
		"domains.verification_token": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return hexOf(32), nil
		},
		"domains.dns_explicit": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return false, nil
		},
		"domains.custom_web_enabled": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			return false, nil
		},
		"marketing_quota_windows.next_send_at": func(rowIdx int, _ map[string]int64, _ int64) (any, error) {
			// §3 sentinel/time variants (rowIdx 1..5): default(-infinity) is
			// exercised by OMITTING the column (rowIdx 1), +infinity, epoch 0,
			// pre-epoch finite, future finite.
			switch rowIdx {
			case 2:
				return "infinity", nil
			case 3:
				return time.Unix(0, 0).UTC(), nil
			case 4:
				return time.UnixMicro(MinFiniteUnixMicro), nil
			default:
				return time.UnixMicro(nowMicros() + 3_600_000_000), nil
			}
		},
		"mailboxes.address": func(rowIdx int, parentLookups map[string]int64, tenantID int64) (any, error) {
			var fqdn string
			if domainID, ok := parentLookups["domain_id"]; ok {
				if err := pool.QueryRow(ctx,
					"SELECT fqdn FROM domains WHERE id=$1", domainID).Scan(&fqdn); err == nil && fqdn != "" {
					return fmt.Sprintf("syn%d@%s", rowIdx, fqdn), nil
				}
			}
			if err := pool.QueryRow(ctx,
				"SELECT fqdn FROM domains WHERE tenant_id=$1 ORDER BY id LIMIT 1", tenantID).Scan(&fqdn); err != nil {
				fqdn = "syn.example.test"
			}
			return fmt.Sprintf("syn%d@%s", rowIdx, fqdn), nil
		},
	}

	valueFor := func(table, col string, cm ColumnMap, _ colMeta, tenantID int64, parentLookups map[string]int64, rowIdx int) (any, error) {
		name := strings.ToLower(col)
		pg := cm.PGType
		if ov, ok := overrides[table+"."+col]; ok {
			return ov(rowIdx, parentLookups, tenantID)
		}
		if pg == "text[]" || strings.HasSuffix(pg, "[]") {
			return []string{"legacy:read"}, nil
		}
		switch {
		case name == "singleton":
			return true, nil
		case pg == "jsonb":
			return "{}", nil
		case pg == "uuid":
			return uuidLike(rowIdx, col), nil
		case pg == "inet":
			return "127.0.0.1", nil
		case pg == "boolean":
			return false, nil
		case pg == "bytea":
			return []byte{0xde, 0xad, 0xbe, byte(rowIdx % 256)}, nil
		case pg == "double precision" || pg == "real":
			return float64(0.5), nil
		case strings.HasPrefix(name, "period_"):
			// Report periods: end must be within [start, start+8 days].
			// Deterministic base: start/end must derive from the SAME instant
			// (two time.Now calls drift by microseconds).
			base := time.UnixMicro(1800000000000000 + int64(rowIdx)*1_000_000)
			if strings.HasSuffix(name, "_end") {
				return base.Add(1 * time.Hour), nil
			}
			return base, nil
		case strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_since") || strings.HasSuffix(name, "_until"):
			if strings.HasPrefix(name, "expires") || strings.HasPrefix(name, "ends") || strings.HasPrefix(name, "lease") ||
				strings.HasPrefix(name, "next_") || strings.HasPrefix(name, "wait") || strings.HasPrefix(name, "valid") ||
				strings.HasPrefix(name, "published") {
				return time.UnixMicro(nowMicros() + int64(rowIdx+1)*3_600_000_000), nil
			}
			return time.UnixMicro(nowMicros() - int64(rowIdx)*1_000_000), nil
		case pg == "text" && (strings.Contains(name, "sha256") || strings.Contains(name, "digest") || strings.Contains(name, "hash")):
			return hexOf(64), nil
		case strings.Contains(name, "request_key"):
			return uuidLike(rowIdx, col), nil
		case name == "email" || strings.HasSuffix(name, "_email"):
			return fmt.Sprintf("syn%d@example.test", counters["email"]+rowIdx), nil
		case name == "address" || strings.HasSuffix(name, "_address"):
			if strings.Contains(name, "ip") {
				return "127.0.0.1", nil
			}
			if id, ok := parentLookups["mailbox_id"]; ok {
				var addr string
				if err := conn.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", id).Scan(&addr); err == nil && addr != "" {
					return addr, nil
				}
			}
			return fmt.Sprintf("syn%d@example.test", counters["email"]+rowIdx), nil
		case name == "fqdn" || strings.HasSuffix(name, "hostname") || strings.HasSuffix(name, "_host") || name == "domain" || strings.HasSuffix(name, "_domain"):
			if strings.Contains(name, "mx") {
				return "mx1.syn.example.test", nil
			}
			return fmt.Sprintf("syn-%s-%d.example.test", table, counters["fqdn:"+table]+rowIdx), nil
		case name == "local_part":
			return fmt.Sprintf("syn%d", counters["local"]+rowIdx), nil
		case name == "day":
			return "2026-01-02", nil
		case pg == "text" && (strings.Contains(name, "token") || strings.Contains(name, "nonce")):
			return hexOf(32), nil
		case strings.Contains(name, "json"):
			return "{}", nil
		case name == "scope":
			if lits := domains.lit(table, col); len(lits) > 0 {
				return lits[(rowIdx-1)%len(lits)], nil
			}
			return "tenant", nil
		case name == "role":
			if lits := domains.lit(table, col); len(lits) > 0 {
				return lits[(rowIdx-1)%len(lits)], nil
			}
			return "owner", nil
		case name == "status" || name == "state" || name == "kind" || name == "mode" || name == "type" || name == "action" || name == "format" || name == "policy":
			if lits := domains.lit(table, col); len(lits) > 0 {
				// Prefer a permissive literal: skip storage/disabled-ish ones
				// that side-gate additional NOT NULL columns.
				for _, lit := range lits {
					switch strings.ToLower(lit) {
					case "storage", "deleted", "disabled", "purge", "purged", "failed", "canceled":
						continue
					}
					return lit, nil
				}
				return lits[(rowIdx-1)%len(lits)], nil
			}
			return "active", nil
		case pg == "integer" || pg == "smallint" || pg == "bigint":
			return int64(1 + rowIdx), nil
		default:
			if lits := domains.lit(table, col); len(lits) > 0 {
				return lits[(rowIdx-1)%len(lits)], nil
			}
			return fmt.Sprintf("syn-%s-%d", table, rowIdx), nil
		}
	}

	for _, ti := range ordered {
		if _, ok := tm[ti.Name]; !ok {
			continue
		}
		if ti.Name == "schema_migrations" || ti.Name == "platform_marketing_dispatch" ||
			ti.Name == "mailbox_storage_reservations" {
			continue // engine bookkeeping / trigger-maintained rows
		}
		if ti.Name == "user_tenant_roles" && len(tenantIDs) > 0 && len(userIDs) > 0 {
			// Two owners per tenant: the primary issues grants, the secondary
			// receives them (self-authorization is rejected).
			inserted := 0
			for i, tenant := range tenantIDs {
				primary := userIDs[i%len(userIDs)]
				secondary := userIDs[(i+1)%len(userIDs)]
				ownersByTenant[tenant] = []int64{primary, secondary}
				for _, u := range []int64{primary, secondary} {
					if _, err := pool.Exec(ctx,
						"INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", u, tenant); err != nil {
						t.Logf("S5 filler: user_tenant_roles pair failed: %v", err)
						continue
					}
					inserted++
				}
			}
			sample.Tables[ti.Name] = inserted
			continue
		}
		if ti.Name == "recall_requests" {
			// Recall previews are service-authored rows: the trigger gates
			// INSERT on current_user = urmail_platform.
			if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
				t.Logf("S5 filler: SET ROLE urmail_platform: %v", err)
			}
		}
		cmTable := tm[ti.Name]
		info := colInfo[ti.Name]
		hasTenantCol := false
		for _, c := range ti.Columns {
			if c == "tenant_id" {
				hasTenantCol = true
			}
		}

		inserted := 0
		var firstSettingsKey string
		singleton := ti.Name == "restore_quarantine" || ti.Name == "tenant_directory_settings" || ti.Name == "domain_dns_security"
		rows := rowsPer
		if ti.Name == "marketing_quota_windows" {
			rows = 5 // five next_send_at time variants (§3 sentinel/time matrix)
		}
		if ti.Name == "mailboxes" || ti.Name == "archive_entries" || ti.Name == "recall_requests" ||
			ti.Name == "ai_usage_requests" {
			// One mailbox/archive-entry pair per tenant (additional mailboxes
			// trip the reserved-address guard, 0024 lifecycle); children keyed
			// to the single mailbox stay within the available parent rows.
			if ti.Name != "archive_entries" {
				rows = 1
			} else {
				rows = 8 // recall chain + terminal anchors
			}
			if ti.Name == "recall_requests" {
				// Three legal recall states: a live preview chain, an initial
				// pending request (no proof), and a re-previewed row whose
				// short expiry elapses before the import (natural PG witness
				// for an expired preview at attempt_epoch 2).
				rows = 7
			}
		}
		if ti.Name == "archive_investigation_sources" || ti.Name == "archive_content" {
			rows = 1 // entry-keyed children: all entries live on tenant 1's mailbox
		}
		// Tables whose FK chain (transitively) reaches mailboxes or
		// archive_entries are clamped to the available parent rows — no
		// orphan or missing-parent attempts (children inherit the chain).
		for _, fk := range ti.FKs {
			if fk.RefTable == "mailboxes" || fk.RefTable == "archive_entries" {
				var parentCount int64
				if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+fk.RefTable).Scan(&parentCount); err == nil && parentCount > 0 && rows > int(parentCount) {
					rows = int(parentCount)
				}
				break
			}
			// Transitively: a parent whose own chain reaches the pair clamps
			// this table to that parent's row count.
			if pt := byNameLookup(plan, fk.RefTable); pt != nil && chainReachesPair(pt, plan, map[string]bool{}) {
				var parentCount int64
				if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+fk.RefTable).Scan(&parentCount); err == nil && parentCount > 0 && rows > int(parentCount) {
					rows = int(parentCount)
				}
				break
			}
		}
		if ti.Name == "archive_entries" {
			// Four entries on tenant 1's mailbox — the recall_requests chain
			// anchors to these (UNIQUE(tenant_id, archive_entry_id)).
			insertedArc := 0
			var anchorMailbox int64
			if err := conn.QueryRow(ctx,
				"SELECT id FROM mailboxes WHERE tenant_id=$1 ORDER BY id LIMIT 1",
				tenantIDs[0]).Scan(&anchorMailbox); err != nil {
				t.Fatalf("S5 filler: anchor mailbox: %v", err)
			}
			for i := 0; i < 8; i++ {
				var arcID int64
				if err := conn.QueryRow(ctx, `INSERT INTO archive_entries
					(tenant_id, mailbox_id, message_id, path, subject, from_addr, to_addr,
					 received_at, size_bytes, legal_hold, deleted_at, created_at)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
					tenantIDs[0], anchorMailbox,
					fmt.Sprintf("syn-msg-%d@x.test", i),
					fmt.Sprintf("cur/%d/syn-%d", i, i),
					fmt.Sprintf("subject %d", i),
					"from@example.test", "to@example.test",
					time.UnixMicro(nowMicros()+int64(i)*1_000_000),
					100+i, false, time.UnixMicro(nowMicros()), time.UnixMicro(nowMicros()),
				).Scan(&arcID); err != nil {
					t.Fatalf("S5 filler: archive_entries row %d: %v", i, err)
				}
				insertedArc++
			}
			var arcCount int64
			_ = conn.QueryRow(ctx, "SELECT count(*) FROM archive_entries").Scan(&arcCount)
			sample.Tables[ti.Name] = insertedArc
			continue
		}
		if ti.Name == "recall_requests" {
			// Legal PG recall lifecycle, losslessly representable in SQLite:
			// 1) service-role INSERT of status='pending' with attempt_epoch
			//    DEFAULT 0 (PG: recall INSERT requires the service role and
			//    status='pending'); 2) owner/admin UPDATE to status='preview'
			//    with attempt_epoch=OLD+1=1 and the full proof (token, digest
			//    = sha256(snapshot), snapshot matching the referenced
			//    archive_entry/mailbox, expiry inside the 5m01s window).
			// SQLite accepts exactly this form on INSERT (status='preview',
			// attempt_epoch=1, proof, target match) — lossless.
			for rowIdx := 1; rowIdx <= rows; rowIdx++ {
				tenantID := int64(0)
				if len(tenantIDs) > 0 {
					// All synthetic archive entries anchor to tenantIDs[0]'s
					// single mailbox; keep every recall row on that tenant.
					tenantID = tenantIDs[0]
				}
				switch rowIdx {
				case 1:
					seedRecallLegalChain(t, ctx, conn, tenantID, rowIdx)
				case 2:
					seedRecallInitialPending(t, ctx, conn, tenantID, rowIdx)
				case 3:
					seedRecallExpiredPreview(ctx, t, conn, tenantID, rowIdx)
				case 7:
					seedRecallLocalRecalled(ctx, t, conn, tenantID, rowIdx)
				default:
					statuses := []string{"failed", "expired", "uncertain"}
					seedRecallTerminal(t, ctx, conn, tenantID, rowIdx, statuses[(rowIdx-4)%3])
				}
				inserted++
			}
			sample.Tables[ti.Name] = inserted
			continue
		}
		for rowIdx := 1; rowIdx <= rows; rowIdx++ {
			if singleton && inserted >= 1 {
				break // single-row table per contract (no duplicate singleton rows)
			}
			chosenTenant := int64(0)
			if hasTenantCol && len(tenantIDs) > 0 {
				chosenTenant = tenantIDs[(rowIdx-1)%len(tenantIDs)]
			}
			parentLookups := map[string]int64{}

			// Production session contract: the tenant/user GUCs back the
			// trigger-level authorization (e.g. compliance owner checks).
			if chosenTenant > 0 {
				gucUser := int64(0)
				if owners, ok := ownersByTenant[chosenTenant]; ok && len(owners) > 0 {
					// The retention family acts AS the grant user (created_by
					// must equal the session user), everything else acts as
					// the primary owner (issuer check).
					if strings.HasPrefix(ti.Name, "retention_") && len(owners) > 1 {
						gucUser = owners[1]
					} else {
						gucUser = owners[0]
					}
				} else {
					var owner sql.NullInt64
					_ = pool.QueryRow(ctx,
						"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
						chosenTenant).Scan(&owner)
					if owner.Valid {
						gucUser = owner.Int64
					}
				}
				if gucUser > 0 {
					_, _ = conn.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,false)", strconv.FormatInt(chosenTenant, 10))
					_, _ = conn.Exec(ctx, "SELECT set_config('urmail.user_id',$1,false)", strconv.FormatInt(gucUser, 10))
				}
			}

			var cols, phs []string
			args := []any{}
			phIdx := 0
			returningID := false
			var failing error

			duplicate := false
			for _, col := range ti.Columns {
				cmCol := cmTable[col]
				if _, mapped := cmTable[col]; !mapped {
					continue
				}
				if ti.Name == "marketing_quota_windows" && col == "next_send_at" && rowIdx == 1 {
					continue // exercise the column DEFAULT (-infinity)
				}
				if ov, ok := overrides[ti.Name+"."+col]; ok {
					v, verr := ov(rowIdx, parentLookups, chosenTenant)
					if verr != nil {
						t.Logf("S5 filler: table %s row %d override %s failed: %v", ti.Name, rowIdx, col, verr)
						continue
					}
					cols = append(cols, col)
					phs = append(phs, fmt.Sprintf("$%d", phIdx+1))
					phIdx++
					args = append(args, v)
					continue
				}
				if col == "tenant_id" && chosenTenant > 0 {
					cols = append(cols, col)
					phs = append(phs, fmt.Sprintf("$%d", phIdx+1))
					phIdx++
					args = append(args, chosenTenant)
					continue
				}
				meta := info[col]
				// Single serial PK: let PG allocate, capture with RETURNING.
				if len(ti.PK) == 1 && ti.PK[0] == col && meta.serial {
					returningID = true
					cols = append(cols, col)
					phs = append(phs, "DEFAULT")
					continue
				}
				// FK column: pick a parent id (tenant-aligned when possible).
				var fkCol *ForeignKey
				for i := range ti.FKs {
					if ti.FKs[i].Column == col {
						fkCol = &ti.FKs[i]
						break
					}
				}
				if fkCol != nil && byNameLookup(plan, fkCol.RefTable) != nil {
					parent := byNameLookup(plan, fkCol.RefTable)
					parentHasTenant := false
					for _, c := range parent.Columns {
						if c == "tenant_id" {
							parentHasTenant = true
						}
					}
					var pid any
					var qerr error
					offset := rowIdx - 1
					if parentHasTenant && chosenTenant > 0 {
						qerr = pool.QueryRow(ctx, fmt.Sprintf(
							"SELECT %s FROM %s WHERE tenant_id=$1 ORDER BY 1 LIMIT 1 OFFSET $2", fkCol.RefColumn, fkCol.RefTable),
							chosenTenant, offset).Scan(&pid)
						if qerr != nil && offset > 0 {
							qerr = pool.QueryRow(ctx, fmt.Sprintf(
								"SELECT %s FROM %s WHERE tenant_id=$1 ORDER BY 1 LIMIT 1", fkCol.RefColumn, fkCol.RefTable),
								chosenTenant).Scan(&pid)
						}
					} else {
						qerr = pool.QueryRow(ctx, fmt.Sprintf(
							"SELECT %s FROM %s ORDER BY 1 LIMIT 1 OFFSET $1", fkCol.RefColumn, fkCol.RefTable),
							offset).Scan(&pid)
						if qerr != nil && offset > 0 {
							qerr = pool.QueryRow(ctx, fmt.Sprintf(
								"SELECT %s FROM %s ORDER BY 1 LIMIT 1", fkCol.RefColumn, fkCol.RefTable)).Scan(&pid)
						}
					}
					if qerr == nil {
						cols = append(cols, col)
						phs = append(phs, fmt.Sprintf("$%d", phIdx+1))
						phIdx++
						args = append(args, pid)
						if col == "mailbox_id" {
							if n, ok := pid.(int64); ok {
								parentLookups["mailbox_id"] = n
							}
						}
						if col == "domain_id" {
							if n, ok := pid.(int64); ok {
								parentLookups["domain_id"] = n
							}
						}
						continue
					}
					// No parent row: if nullable, skip (NULL); else the row
					// cannot be built — record failure for this row.
					if meta.nullable {
						continue
					}
					failing = fmt.Errorf("column %s: no parent row in %s", col, fkCol.RefTable)
					break
				}
				// tenant_id: use the chosen tenant.
				if col == "tenant_id" && chosenTenant > 0 {
					cols = append(cols, col)
					phs = append(phs, fmt.Sprintf("$%d", phIdx+1))
					phIdx++
					args = append(args, chosenTenant)
					continue
				}
				mustSupply := !meta.nullable && !meta.hasDefault
				isPK := false
				for _, pk := range ti.PK {
					if pk == col {
						isPK = true
					}
				}
				if !mustSupply && !isPK {
					continue // let defaults/NULL apply
				}
				v, verr := valueFor(ti.Name, col, cmCol, meta, chosenTenant, parentLookups, rowIdx)
				if verr != nil || v == nil {
					v = fmt.Sprintf("syn-%s-%d", col, rowIdx)
				}
				_ = verr
				if col == "key" && ti.Name == "settings" && rowIdx == 1 {
					if s, ok := v.(string); ok {
						firstSettingsKey = s
					}
				}
				cols = append(cols, col)
				phs = append(phs, fmt.Sprintf("$%d", phIdx+1))
				phIdx++
				args = append(args, v)
			}
			if failing != nil {
				t.Fatalf("S5 filler: table %s row %d skipped: %v", ti.Name, rowIdx, failing)
			}
			if len(cols) == 0 {
				continue
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", ti.Name, quoteIdentList(cols), strings.Join(phs, ","))
			if returningID {
				q += " RETURNING id"
				var id int64
				if err := conn.QueryRow(ctx, q, args...).Scan(&id); err != nil {
					t.Fatalf("S5 filler: table %s row %d failed: %v (sql=%s args=%v)", ti.Name, rowIdx, err, q, args)
				}
				ids[ti.Name] = append(ids[ti.Name], id)
				if ti.Name == "domains" {
					// The PG onboarding trigger forces provisioning_state=
					// 'pending' on INSERT; the provisioning completion runs
					// under the migration/platform role (guard exemption) —
					// mirror that final state.
					if _, err := conn.Exec(ctx, "SET ROLE urmail_migrate"); err != nil {
						t.Fatalf("S5 filler: SET ROLE urmail_migrate: %v", err)
					}
					_, err := conn.Exec(ctx,
						"UPDATE domains SET provisioning_state='ready' WHERE id=$1::bigint", id)
					if _, rerr := conn.Exec(ctx, "RESET ROLE"); rerr != nil {
						t.Fatalf("reset role: %v", rerr)
					}
					if err != nil {
						t.Fatalf("S5 filler: domain provisioning update: %v", err)
					}
				}
				if ti.Name == "tenants" {
					tenantIDs = append(tenantIDs, id)
				}
				if ti.Name == "users" {
					userIDs = append(userIDs, id)
				}
				if ti.Name == "compliance_grants" {
					if owners, ok := ownersByTenant[chosenTenant]; ok && len(owners) > 1 {
						// the grantee is the secondary owner: this is the
						// retention_manage grant the secondary owner uses
						grantIDByTenant[chosenTenant] = id
					}
				}
			} else {
				if _, err := conn.Exec(ctx, q, args...); err != nil {
					t.Fatalf("S5 filler: table %s row %d failed: %v (sql=%s args=%v)", ti.Name, rowIdx, err, q, args)
				}
			}
			inserted++
			if duplicate {
				break
			}
		}
		if ti.Name == "recall_requests" {
			if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
				t.Fatalf("reset role: %v", err)
			}
		}
		if ti.Name == "settings" && firstSettingsKey != "" {
			sample.SettingsKey = firstSettingsKey
		}
		sample.Tables[ti.Name] = inserted
	}
	sample.TenantIDs = tenantIDs
	return sample
}

func quoteIdentList(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = `"` + c + `"`
	}
	return strings.Join(parts, ",")
}

func nowMicros() int64 { return time.Now().UTC().UnixMicro() }

// chainReachesPair reports whether the table's FK chain (transitively)
// reaches the mailboxes/archive_entries anchor pair.
func chainReachesPair(ti *TableInfo, plan []TableInfo, seen map[string]bool) bool {
	if ti == nil || seen[ti.Name] {
		return false
	}
	seen[ti.Name] = true
	for _, fk := range ti.FKs {
		if fk.RefTable == "mailboxes" || fk.RefTable == "archive_entries" {
			return true
		}
		if chainReachesPair(byNameLookup(plan, fk.RefTable), plan, seen) {
			return true
		}
	}
	return false
}

func byNameLookup(plan []TableInfo, name string) *TableInfo {
	for i := range plan {
		if plan[i].Name == name {
			return &plan[i]
		}
	}
	return nil
}

func sha256OfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// seedRecallExpiredPreview creates a legal preview at attempt_epoch 1 and
// re-previews it at epoch 2 with a 2-second expiry, then returns. The caller
// waits until the natural expiry elapses before importing, so the PG source
// holds a genuinely expired preview (no trigger edits, no deletes).
func seedRecallExpiredPreview(ctx context.Context, t testingT, conn *pgxpool.Conn, tenantID int64, rowIdx int) {
	var entryID, mailboxID int64
	var mid string
	if err := conn.QueryRow(ctx,
		"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1 OFFSET $2",
		tenantID, rowIdx-1).Scan(&entryID, &mailboxID, &mid); err != nil {
		t.Fatalf("S5 recall expiry: archive entry: %v", err)
	}
	var addr string
	if err := conn.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", mailboxID).Scan(&addr); err != nil {
		t.Fatalf("S5 recall expiry: mailbox address: %v", err)
	}
	var owner int64
	if err := conn.QueryRow(ctx,
		"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
		tenantID).Scan(&owner); err != nil {
		t.Fatalf("S5 recall expiry: owner: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 recall expiry: SET ROLE: %v", err)
	}
	var recallID int64
	if err := conn.QueryRow(ctx,
		"INSERT INTO recall_requests(tenant_id, message_id, archive_entry_id, mailbox_id, requested_by) VALUES($1,$2,$3,$4,$5) RETURNING id",
		tenantID, mid, entryID, mailboxID, owner).Scan(&recallID); err != nil {
		t.Fatalf("S5 recall expiry: pending insert: %v", err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 recall expiry: RESET ROLE: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,false), set_config('urmail.user_id',$2,false)",
		strconv.FormatInt(tenantID, 10), strconv.FormatInt(owner, 10)); err != nil {
		t.Fatalf("S5 recall expiry: set GUC: %v", err)
	}
	snapshot := fmt.Sprintf(`{"tenant_id":%d,"actor_id":%d,"entry_id":%d,"mailbox_id":%d,"address":%q,"message_id":%q,"copies":[]}`,
		tenantID, owner, entryID, mailboxID, addr, mid)
	digest := sha256OfBytes([]byte(snapshot))
	short := time.UnixMicro(nowMicros() + 2_000_000)
	if _, err := conn.Exec(ctx, `UPDATE recall_requests SET status='preview', preview_actor_id=$1,
		preview_token=$2, preview_digest=$3, preview_snapshot=$4, preview_expires_at=$5, attempt_epoch=1
		WHERE id=$6`, owner, fmt.Sprintf("00000000-0000-0000-0000-%012x", rowIdx), digest, []byte(snapshot), short, recallID); err != nil {
		t.Fatalf("S5 recall expiry: epoch1 preview: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE recall_requests SET preview_token=$1, preview_digest=$2,
		preview_snapshot=$3, preview_expires_at=$4, attempt_epoch=2 WHERE id=$5`,
		fmt.Sprintf("11111111-0000-0000-0000-%012x", rowIdx), digest, []byte(snapshot), short, recallID); err != nil {
		t.Fatalf("S5 recall expiry: epoch2 re-preview: %v", err)
	}
}

// seedRecallLocalRecalled drives the current product lifecycle to
// local_recalled: service pending insert, owner preview with one copy in the
// snapshot, owner confirmation selecting that copy, then the platform
// completion with completed_at (the only legal local_recalled path).
func seedRecallLocalRecalled(ctx context.Context, t testingT, conn *pgxpool.Conn, tenantID int64, rowIdx int) {
	var entryID, mailboxID int64
	var mid string
	if err := conn.QueryRow(ctx,
		"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1 OFFSET $2",
		tenantID, rowIdx-1).Scan(&entryID, &mailboxID, &mid); err != nil {
		t.Fatalf("S5 local_recalled: archive entry: %v", err)
	}
	var addr string
	if err := conn.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", mailboxID).Scan(&addr); err != nil {
		t.Fatalf("S5 local_recalled: address: %v", err)
	}
	var owner int64
	if err := conn.QueryRow(ctx,
		"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
		tenantID).Scan(&owner); err != nil {
		t.Fatalf("S5 local_recalled: owner: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 local_recalled: SET ROLE: %v", err)
	}
	var recallID int64
	if err := conn.QueryRow(ctx,
		"INSERT INTO recall_requests(tenant_id, message_id, archive_entry_id, mailbox_id, requested_by) VALUES($1,$2,$3,$4,$5) RETURNING id",
		tenantID, mid, entryID, mailboxID, owner).Scan(&recallID); err != nil {
		t.Fatalf("S5 local_recalled: pending insert: %v", err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 local_recalled: RESET ROLE: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,false), set_config('urmail.user_id',$2,false)",
		strconv.FormatInt(tenantID, 10), strconv.FormatInt(owner, 10)); err != nil {
		t.Fatalf("S5 local_recalled: GUC: %v", err)
	}
	snapshot := fmt.Sprintf(`{"tenant_id":%d,"actor_id":%d,"entry_id":%d,"mailbox_id":%d,"address":%q,"message_id":%q,"copies":[{"guid":"local-copy","uid_validity":1,"uid":1,"message_guid":"local-copy"}]}`,
		tenantID, owner, entryID, mailboxID, addr, mid)
	digest := sha256OfBytes([]byte(snapshot))
	if _, err := conn.Exec(ctx, `UPDATE recall_requests SET status='preview', preview_actor_id=$1,
		preview_token=$2, preview_digest=$3, preview_snapshot=$4, preview_expires_at=$5, attempt_epoch=1 WHERE id=$6`,
		owner, fmt.Sprintf("dddddddd-0000-0000-0000-%012x", rowIdx), digest, []byte(snapshot), time.UnixMicro(nowMicros()+240_000_000), recallID); err != nil {
		t.Fatalf("S5 local_recalled: preview: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE recall_requests SET status='pending', confirmed_at=now(),
		selected_copy_keys='["local-copy:1:1"]' WHERE id=$1`, recallID); err != nil {
		t.Fatalf("S5 local_recalled: confirm: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 local_recalled: SET ROLE completion: %v", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE recall_requests SET status='local_recalled', completed_at=now() WHERE id=$1", recallID); err != nil {
		t.Fatalf("S5 local_recalled: completion: %v", err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 local_recalled: final RESET ROLE: %v", err)
	}
}

// seedRecallTerminal creates a legal terminal recall row the way the PG
// service does: a pending insert under the service role, then a terminal
// transition under the same role. The staging import must preserve it.
func seedRecallTerminal(t testingT, ctx context.Context, conn *pgxpool.Conn, tenantID int64, rowIdx int, status string) {
	var entryID, mailboxID int64
	var mid string
	if err := conn.QueryRow(ctx,
		"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1 OFFSET $2",
		tenantID, rowIdx-1).Scan(&entryID, &mailboxID, &mid); err != nil {
		t.Fatalf("S5 recall terminal: archive entry: %v", err)
	}
	var owner int64
	if err := conn.QueryRow(ctx,
		"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
		tenantID).Scan(&owner); err != nil {
		t.Fatalf("S5 recall terminal: owner: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 recall terminal: SET ROLE: %v", err)
	}
	var recallID int64
	if err := conn.QueryRow(ctx,
		"INSERT INTO recall_requests(tenant_id, message_id, archive_entry_id, mailbox_id, requested_by) VALUES($1,$2,$3,$4,$5) RETURNING id",
		tenantID, mid, entryID, mailboxID, owner).Scan(&recallID); err != nil {
		t.Fatalf("S5 recall terminal: pending insert: %v", err)
	}
	if _, err := conn.Exec(ctx,
		"UPDATE recall_requests SET status=$1, completed_at=now() WHERE id=$2", status, recallID); err != nil {
		t.Fatalf("S5 recall terminal %s: %v", status, err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 recall terminal: RESET ROLE: %v", err)
	}
}

// seedRecallInitialPending inserts one legal initial pending recall_requests
// row under the service role: status='pending', attempt_epoch DEFAULT 0, no
// preview proof. The SQLite insert guard rejects this state; the offline
// preservation import must keep it verbatim.
func seedRecallInitialPending(t testingT, ctx context.Context, conn *pgxpool.Conn, tenantID int64, rowIdx int) {
	var entryID, mailboxID int64
	var mid string
	if err := conn.QueryRow(ctx,
		"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1 OFFSET $2",
		tenantID, rowIdx-1).Scan(&entryID, &mailboxID, &mid); err != nil {
		t.Fatalf("S5 recall pending: archive entry: %v", err)
	}
	var owner int64
	if err := conn.QueryRow(ctx,
		"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
		tenantID).Scan(&owner); err != nil {
		t.Fatalf("S5 recall pending: owner: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 recall pending: SET ROLE urmail_platform: %v", err)
	}
	if _, err := conn.Exec(ctx,
		"INSERT INTO recall_requests(tenant_id, message_id, archive_entry_id, mailbox_id, requested_by) VALUES($1,$2,$3,$4,$5)",
		tenantID, mid, entryID, mailboxID, owner); err != nil {
		t.Fatalf("S5 recall pending: insert: %v", err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 recall pending: RESET ROLE: %v", err)
	}
}

// seedRecallLegalChain drives one recall_requests row through the legal PG
// lifecycle so the row is losslessly representable in the SQLite target:
// step 1 inserts a status='pending' request under the service role (the only
// PG-legal INSERT path), step 2 authorizes the preview under the tenant
// owner/admin session with the complete proof (token, digest =
// sha256(snapshot), snapshot bound to the referenced archive entry/mailbox,
// expiry inside the 5m01s window) and attempt_epoch = OLD+1 = 1 — which is
// exactly the form the SQLite recall_state_guard_insert accepts.
func seedRecallLegalChain(t testingT, ctx context.Context, conn *pgxpool.Conn, tenantID int64, rowIdx int) {
	var entryID, mailboxID int64
	var mid string
	if err := conn.QueryRow(ctx,
		"SELECT id, mailbox_id, message_id FROM archive_entries WHERE tenant_id=$1 ORDER BY id LIMIT 1 OFFSET $2",
		tenantID, rowIdx-1).Scan(&entryID, &mailboxID, &mid); err != nil {
		t.Fatalf("S5 recall chain: archive entry: %v", err)
	}
	var addr string
	if err := conn.QueryRow(ctx, "SELECT address FROM mailboxes WHERE id=$1", mailboxID).Scan(&addr); err != nil {
		t.Fatalf("S5 recall chain: mailbox address: %v", err)
	}
	var owner int64
	if err := conn.QueryRow(ctx,
		"SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' ORDER BY user_id LIMIT 1",
		tenantID).Scan(&owner); err != nil {
		t.Fatalf("S5 recall chain: owner: %v", err)
	}
	// Step 1: service-role pending insert.
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatalf("S5 recall chain: SET ROLE urmail_platform: %v", err)
	}
	var recallID int64
	if err := conn.QueryRow(ctx,
		"INSERT INTO recall_requests(tenant_id, message_id, archive_entry_id, mailbox_id, requested_by) VALUES($1,$2,$3,$4,$5) RETURNING id",
		tenantID, mid, entryID, mailboxID, owner).Scan(&recallID); err != nil {
		t.Fatalf("S5 recall chain: pending insert: %v", err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("S5 recall chain: RESET ROLE: %v", err)
	}
	// The preview-authority guard reads the session GUC user/tenant.
	if _, err := conn.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,false), set_config('urmail.user_id',$2,false)",
		strconv.FormatInt(tenantID, 10), strconv.FormatInt(owner, 10)); err != nil {
		t.Fatalf("S5 recall chain: set GUC: %v", err)
	}
	// Step 2: owner-authorized preview with complete proof (epoch 0+1=1).
	snapshot := fmt.Sprintf(`{"tenant_id":%d,"actor_id":%d,"entry_id":%d,"mailbox_id":%d,"address":%q,"message_id":%q,"copies":[]}`,
		tenantID, owner, entryID, mailboxID, addr, mid)
	digest := sha256OfBytes([]byte(snapshot))
	token := strings.Repeat("a", 8) + fmt.Sprintf("%x", rowIdx) + strings.Repeat("b", 36-8-len(fmt.Sprintf("%x", rowIdx)))
	if _, err := conn.Exec(ctx, `UPDATE recall_requests SET status='preview', preview_actor_id=$1,
		preview_token=$2, preview_digest=$3, preview_snapshot=$4, preview_expires_at=$5, attempt_epoch=1
		WHERE id=$6`, owner, token, digest, []byte(snapshot),
		time.UnixMicro(nowMicros()+240_000_000), recallID); err != nil {
		t.Fatalf("S5 recall chain: preview update: %v", err)
	}
}

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/domain/selfhost"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// runBootstrap creates the cluster-level roles. It is intentionally separate
// from "migrate" because it must run as a PostgreSQL superuser (or a role with
// CREATEROLE/BYPASSRLS), while the schema migrations run as urmail_migrate.
func runBootstrap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	connString := fs.String("database-url", os.Getenv("URMAIL_BOOTSTRAP_DATABASE_URL"), "superuser database URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *connString == "" {
		return errors.New("bootstrap requires --database-url or URMAIL_BOOTSTRAP_DATABASE_URL")
	}
	if err := db.Bootstrap(ctx, *connString); err != nil {
		return err
	}
	fmt.Println("cluster roles bootstrapped")
	return nil
}

// runSetup creates the first brand, tenant, owner and platform administrator.
// It is idempotent: an existing user is kept unless --reset-password is given.
func runSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	email := fs.String("email", "", "initial administrator email")
	password := fs.String("password", os.Getenv("URMAIL_SETUP_PASSWORD"), "initial administrator password (or URMAIL_SETUP_PASSWORD)")
	brandSlug := fs.String("brand", "default", "brand slug")
	tenantSlug := fs.String("tenant", "default", "tenant slug")
	tenantName := fs.String("tenant-name", "Default", "tenant display name")
	domainName := fs.String("domain", "", "create the first domain (with the product DKIM key) for an existing administrator")
	freeUntil := fs.String("free-until", "", "explicit finite free grant end in RFC3339; absent keeps a new tenant pending")
	freeReason := fs.String("free-reason", "", "audited reason for an explicit finite free grant")
	resetPassword := fs.Bool("reset-password", false, "reset the password when the user already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(*email))
	if normalizedEmail == "" || (*password == "" && *domainName == "") {
		return errors.New("setup requires --email and --password")
	}
	addr, err := mail.ParseAddress(normalizedEmail)
	if err != nil || strings.ToLower(addr.Address) != normalizedEmail {
		return fmt.Errorf("invalid administrator email %q", *email)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// setup owns the platform surface (brand/tenant/owner/platform admin); the
	// store enforces the platform scope, so bind it at this trusted CLI entry.
	// URMAIL_SETUP_SCOPE is the deployment negative control: any non-platform
	// value must be refused before a write, never honored.
	setupScope := isolation.ScopePlatform
	if raw := strings.TrimSpace(os.Getenv("URMAIL_SETUP_SCOPE")); raw != "" && raw != "platform" {
		setupScope = isolation.ScopeTenant
	}
	ctx = isolation.WithScope(ctx, isolation.ScopeContext{Scope: setupScope, TenantID: conditionalTenant(setupScope)})
	if *domainName != "" {
		if normalizedEmail == "" {
			return errors.New("setup --domain requires --email")
		}
		return runSetupFirstDomain(ctx, cfg, *domainName, normalizedEmail)
	}
	if err := isolation.CheckContext(ctx, "PlatformSetupTenant"); err != nil {
		return err
	}
	input := selfhost.Input{DatabaseURL: cfg.DatabaseURLPlatform, AdminEmail: normalizedEmail, Password: *password, Hostname: cfg.MyHostname}
	if cfg.IsSQLite() {
		// Same credential and hostname rules as the PostgreSQL preflight, from
		// the same source. The DSN shape is not meaningful in single-engine
		// mode, so a canonical valid PostgreSQL URL feeds only those rules.
		if err := selfhost.Preflight(selfhost.Input{DatabaseURL: "postgres://urmail@localhost/urmail", AdminEmail: normalizedEmail, Password: *password, Hostname: cfg.MyHostname}); err != nil {
			return selfhost.ErrNotReady
		}
	} else if err := selfhost.PreflightConnected(ctx, input, func(ctx context.Context, databaseURL string) error {
		pool, err := db.OpenPlatform(ctx, databaseURL)
		if err != nil {
			return err
		}
		pool.Close()
		return nil
	}); err != nil {
		return err
	}
	if !slugPattern.MatchString(*brandSlug) {
		return fmt.Errorf("invalid brand slug %q", *brandSlug)
	}
	if !slugPattern.MatchString(*tenantSlug) {
		return fmt.Errorf("invalid tenant slug %q", *tenantSlug)
	}
	if strings.TrimSpace(*tenantName) == "" {
		return errors.New("tenant name cannot be empty")
	}

	var freeEnd time.Time
	if *freeUntil != "" {
		end, parseErr := time.Parse(time.RFC3339, *freeUntil)
		if parseErr != nil || !end.After(time.Now()) || len(strings.TrimSpace(*freeReason)) < 10 || len(*freeReason) > 2000 {
			return errors.New("--free-until requires a future RFC3339 date and an evidence reason of 10 to 2000 characters")
		}
		freeEnd = end.UTC()
	} else if strings.TrimSpace(*freeReason) != "" {
		return errors.New("--free-reason requires --free-until")
	}
	if cfg.IsSQLite() {
		return runSetupSQLite(ctx, cfg, *brandSlug, *tenantSlug, *tenantName, normalizedEmail, *password, *resetPassword, freeEnd, *freeReason)
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return fmt.Errorf("open platform database: %w", err)
	}
	defer pool.Close()

	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return fmt.Errorf("begin setup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SQLite uses qmark placeholders; the scoped pool passes SQL through, so
	// the few raw statements are written per dialect.
	ph := func(n int) string {
		if cfg.IsSQLite() {
			return "?"
		}
		return "$" + strconv.Itoa(n)
	}
	var brandID int64
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO brands (slug, name)
		VALUES (%s, %s)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, ph(1), ph(2)), *brandSlug, *brandSlug).Scan(&brandID); err != nil {
		return fmt.Errorf("upsert brand: %w", err)
	}

	q := db.Queries(tx)
	tenant, err := q.PlatformSetupTenant(ctx, store.PlatformSetupTenantParams{BrandID: brandID, Name: *tenantName, Slug: *tenantSlug})
	if err != nil {
		return fmt.Errorf("upsert tenant: %w", err)
	}
	tenantID := tenant.ID
	if err = q.PlatformLockBillingTenant(ctx, tenantID); err != nil {
		return err
	}
	if !freeEnd.IsZero() {
		if _, err = tx.Exec(ctx, "UPDATE tenants SET status='active' WHERE id="+ph(1), tenantID); err != nil {
			return err
		}
	}

	var userID int64
	err = tx.QueryRow(ctx, "SELECT id FROM users WHERE email = "+ph(1), normalizedEmail).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows):
		hash, hashErr := panelpassword.Hash(*password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO users (email, password_hash, display_name, status)
			VALUES (%s, %s, 'Administrator', 'active')
			RETURNING id`, ph(1), ph(2)), normalizedEmail, hash).Scan(&userID); err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up administrator: %w", err)
	case *resetPassword:
		hash, hashErr := panelpassword.Hash(*password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE users SET password_hash = %s, status = 'active' WHERE id = %s", ph(2), ph(1)), userID, hash); err != nil {
			return fmt.Errorf("reset administrator password: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO user_tenant_roles (user_id, tenant_id, role)
		VALUES (%s, %s, 'owner')
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = 'owner'`, ph(1), ph(2)), userID, tenantID); err != nil {
		return fmt.Errorf("grant tenant owner role: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE tenants SET owner_user_id = %s
		WHERE id = %s AND (owner_user_id IS NULL OR owner_user_id <> %s)`, ph(2), ph(1), ph(2)), tenantID, userID); err != nil {
		return fmt.Errorf("set tenant owner: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO platform_admins (user_id) VALUES (%s)
		ON CONFLICT (user_id) DO NOTHING`, ph(1)), userID); err != nil {
		return fmt.Errorf("grant platform administrator: %w", err)
	}
	if err = q.PlatformDemotePreviousOwners(ctx, store.PlatformDemotePreviousOwnersParams{TenantID: tenantID, UserID: userID}); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "local_operator", "effective_uid": os.Geteuid(), "source": "bootstrap", "status": 200, "outcome": "succeeded", "created_tenant": tenant.Created, "finite_free_grant": !freeEnd.IsZero(), "free_until": freeEnd, "grant_reason": strings.TrimSpace(*freeReason)})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Action: "SETUP platform.bootstrap", TargetType: pgtype.Text{String: "tenant", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(tenantID, 10), Valid: true}, Detail: detail}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup: %w", err)
	}

	fmt.Printf("setup complete: tenant=%s brand=%s user=%s platform_admin=true\n", *tenantSlug, *brandSlug, normalizedEmail)
	return nil
}

// runSetupSQLite creates the first brand, tenant, owner and platform
// administrator in single-engine SQLite mode. The legacy runtime store only
// carries PostgreSQL statements for these setup operations, so this branch
// uses the same raw statements as the PostgreSQL path with qmark
// placeholders; the platform scope is already bound by the caller.
func runSetupSQLite(ctx context.Context, cfg *config.Config, brandSlug, tenantSlug, tenantName, email, password string, resetPassword bool, freeEnd time.Time, freeReason string) error {
	if strings.TrimSpace(cfg.SQLitePath) == "" {
		return errors.New("SQLite setup requires URMAIL_SQLITE_PATH")
	}
	sqliteDB, err := sqlitedb.OpenExisting(ctx, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion)
	if err != nil {
		return fmt.Errorf("open verified sqlite database: %w", err)
	}
	defer func() { _ = sqliteDB.Close() }()
	pool, err := db.NewSQLitePool(sqliteDB, isolation.ScopePlatform)
	if err != nil {
		return fmt.Errorf("open platform sqlite pool: %w", err)
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return fmt.Errorf("begin setup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var brandID int64
	if err = tx.QueryRow(ctx, `INSERT INTO brands (slug, name) VALUES (?, ?)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`, brandSlug, brandSlug).Scan(&brandID); err != nil {
		return fmt.Errorf("upsert brand: %w", err)
	}
	var existingTenant int64
	tenantExists := true
	if qerr := tx.QueryRow(ctx, `SELECT id FROM tenants WHERE brand_id=? AND slug=?`, brandID, tenantSlug).Scan(&existingTenant); errors.Is(qerr, sql.ErrNoRows) || errors.Is(qerr, pgx.ErrNoRows) {
		tenantExists = false
	} else if qerr != nil {
		return fmt.Errorf("look up tenant: %w", qerr)
	}
	var tenantID int64
	if err = tx.QueryRow(ctx, `INSERT INTO tenants (brand_id, name, slug, status) VALUES (?, ?, ?, 'pending')
		ON CONFLICT (brand_id, slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`, brandID, tenantName, tenantSlug).Scan(&tenantID); err != nil {
		return fmt.Errorf("upsert tenant: %w", err)
	}
	if !freeEnd.IsZero() {
		if _, err = tx.Exec(ctx, `UPDATE tenants SET status='active' WHERE id=?`, tenantID); err != nil {
			return err
		}
	}
	var userID int64
	lookup := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID)
	switch {
	case errors.Is(lookup, sql.ErrNoRows) || errors.Is(lookup, pgx.ErrNoRows):
		hash, hashErr := panelpassword.Hash(password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if err = tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, display_name, status)
			VALUES (?, ?, 'Administrator', 'active') RETURNING id`, email, hash).Scan(&userID); err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
	case lookup != nil:
		return fmt.Errorf("look up administrator: %w", lookup)
	case resetPassword:
		hash, hashErr := panelpassword.Hash(password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET password_hash = ?, status = 'active' WHERE id = ?`, hash, userID); err != nil {
			return fmt.Errorf("reset administrator password: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_tenant_roles (user_id, tenant_id, role) VALUES (?, ?, 'owner')
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = 'owner'`, userID, tenantID); err != nil {
		return fmt.Errorf("grant tenant owner role: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tenants SET owner_user_id = ? WHERE id = ? AND (owner_user_id IS NULL OR owner_user_id <> ?)`, userID, tenantID, userID); err != nil {
		return fmt.Errorf("set tenant owner: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform_admins (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return fmt.Errorf("grant platform administrator: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE user_tenant_roles SET role='admin' WHERE tenant_id=? AND role='owner' AND user_id<>?`, tenantID, userID); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "local_operator", "effective_uid": os.Geteuid(), "source": "bootstrap", "status": 200, "outcome": "succeeded", "created_tenant": !tenantExists, "finite_free_grant": !freeEnd.IsZero(), "free_until": freeEnd, "grant_reason": strings.TrimSpace(freeReason)})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs (tenant_id, actor_user_id, action, target_type, target_id, detail)
		VALUES (?, ?, 'SETUP platform.bootstrap', 'tenant', ?, ?)`, tenantID, userID, strconv.FormatInt(tenantID, 10), string(detail)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup: %w", err)
	}
	fmt.Printf("setup complete: tenant=%s brand=%s user=%s platform_admin=true\n", tenantSlug, brandSlug, email)
	return nil
}

func conditionalTenant(scope isolation.Scope) int64 {
	if scope == isolation.ScopeTenant {
		return 1 // explicit non-platform negative control; the platform method refuses
	}
	return 0
}

// runSetupFirstDomain creates the first domain for an existing administrator
// using the product DKIM generator and the same persisted columns as the
// domain API. It is idempotent and works on both engines.
func runSetupFirstDomain(ctx context.Context, cfg *config.Config, fqdn, email string) error {
	fqdn = strings.ToLower(strings.TrimSpace(fqdn))
	if fqdn == "" {
		return errors.New("setup --domain requires a domain name")
	}
	var raw *sql.DB
	if cfg.IsSQLite() {
		if strings.TrimSpace(cfg.SQLitePath) == "" {
			return errors.New("SQLite setup requires URMAIL_SQLITE_PATH")
		}
		db, openErr := sqlitedb.OpenExisting(ctx, cfg.SQLitePath, sqlitedb.ExpectedSchemaVersion)
		if openErr != nil {
			return fmt.Errorf("open verified sqlite database: %w", openErr)
		}
		defer func() { _ = db.Close() }()
		raw = db
	} else {
		return errors.New("setup --domain is only supported in SQLite single-engine mode")
	}
	q := func(query string, args ...any) (int64, error) {
		var id int64
		err := raw.QueryRowContext(ctx, query, args...).Scan(&id)
		return id, err
	}
	tenantID, err := q(`SELECT t.id FROM tenants t JOIN users u ON u.id=t.owner_user_id WHERE u.email=? LIMIT 1`, email)
	if err != nil {
		return fmt.Errorf("setup --domain requires an existing administrator tenant: %w", err)
	}
	var existing int64
	if qerr := raw.QueryRowContext(ctx, `SELECT id FROM domains WHERE fqdn=?`, fqdn).Scan(&existing); qerr == nil {
		fmt.Printf("setup domain already present: fqdn=%s\n", fqdn)
		return nil
	} else if !errors.Is(qerr, sql.ErrNoRows) {
		return fmt.Errorf("look up domain: %w", qerr)
	}
	dkim, err := domainMail.GenerateDKIMKeypair("mail")
	if err != nil {
		return fmt.Errorf("generate DKIM key: %w", err)
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	verification := hex.EncodeToString(token)
	if _, err = raw.ExecContext(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,provisioning_state,dkim_selector,dkim_private_key)
		VALUES(?,?, 'pending_dns',?,'pending',?,?)`, tenantID, fqdn, verification, dkim.Selector, dkim.PrivateKey); err != nil {
		return fmt.Errorf("create first domain: %w", err)
	}
	fmt.Printf("setup domain complete: fqdn=%s tenant=%d dkim_selector=%s\n", fqdn, tenantID, dkim.Selector)
	return nil
}

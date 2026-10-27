package main

import (
	"context"
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
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/domain/selfhost"
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
	freeUntil := fs.String("free-until", "", "explicit finite free grant end in RFC3339; absent keeps a new tenant pending")
	freeReason := fs.String("free-reason", "", "audited reason for an explicit finite free grant")
	resetPassword := fs.Bool("reset-password", false, "reset the password when the user already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(*email))
	if normalizedEmail == "" || *password == "" {
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
	input := selfhost.Input{DatabaseURL: cfg.DatabaseURLPlatform, AdminEmail: normalizedEmail, Password: *password, Hostname: cfg.MyHostname}
	if err := selfhost.PreflightConnected(ctx, input, func(ctx context.Context, databaseURL string) error {
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

	freeEnd := pgtype.Timestamptz{}
	if *freeUntil != "" {
		end, parseErr := time.Parse(time.RFC3339, *freeUntil)
		if parseErr != nil || !end.After(time.Now()) || len(strings.TrimSpace(*freeReason)) < 10 || len(*freeReason) > 2000 {
			return errors.New("--free-until requires a future RFC3339 date and an evidence reason of 10 to 2000 characters")
		}
		freeEnd = pgtype.Timestamptz{Time: end.UTC(), Valid: true}
	} else if strings.TrimSpace(*freeReason) != "" {
		return errors.New("--free-reason requires --free-until")
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

	var brandID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO brands (slug, name)
		VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, *brandSlug, *brandSlug).Scan(&brandID); err != nil {
		return fmt.Errorf("upsert brand: %w", err)
	}

	q := store.New(tx)
	tenant, err := q.PlatformSetupTenant(ctx, store.PlatformSetupTenantParams{BrandID: brandID, Name: *tenantName, Slug: *tenantSlug})
	if err != nil {
		return fmt.Errorf("upsert tenant: %w", err)
	}
	tenantID := tenant.ID
	if err = q.PlatformLockBillingTenant(ctx, tenantID); err != nil {
		return err
	}
	if freeEnd.Valid {
		changed, grantErr := q.PlatformGrantSetupFreeTerm(ctx, store.PlatformGrantSetupFreeTermParams{TenantID: tenantID, PeriodEnd: freeEnd})
		if grantErr != nil {
			return grantErr
		}
		if changed != 1 {
			return errors.New("finite bootstrap gift would replace existing non-free rights; no changes committed")
		}
	} else if tenant.Created {
		if err = q.PlatformRemoveRegistrationGift(ctx, tenantID); err != nil {
			return err
		}
	}

	var userID int64
	err = tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", normalizedEmail).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		hash, hashErr := panelpassword.Hash(*password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (email, password_hash, display_name, status)
			VALUES ($1, $2, 'Administrator', 'active')
			RETURNING id`, normalizedEmail, hash).Scan(&userID); err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
	case err != nil:
		return fmt.Errorf("look up administrator: %w", err)
	case *resetPassword:
		hash, hashErr := panelpassword.Hash(*password)
		if hashErr != nil {
			return fmt.Errorf("hash administrator password: %w", hashErr)
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET password_hash = $2, status = 'active' WHERE id = $1", userID, hash); err != nil {
			return fmt.Errorf("reset administrator password: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_tenant_roles (user_id, tenant_id, role)
		VALUES ($1, $2, 'owner')
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = 'owner'`, userID, tenantID); err != nil {
		return fmt.Errorf("grant tenant owner role: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tenants SET owner_user_id = $2
		WHERE id = $1 AND (owner_user_id IS NULL OR owner_user_id <> $2)`, tenantID, userID); err != nil {
		return fmt.Errorf("set tenant owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform_admins (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return fmt.Errorf("grant platform administrator: %w", err)
	}
	if err = q.PlatformDemotePreviousOwners(ctx, store.PlatformDemotePreviousOwnersParams{TenantID: tenantID, UserID: userID}); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "local_operator", "effective_uid": os.Geteuid(), "source": "bootstrap", "status": 200, "outcome": "succeeded", "created_tenant": tenant.Created, "finite_free_grant": freeEnd.Valid, "free_until": freeEnd, "grant_reason": strings.TrimSpace(*freeReason)})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Action: "SETUP platform.bootstrap", TargetType: pgtype.Text{String: "tenant", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(tenantID, 10), Valid: true}, Detail: detail}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup: %w", err)
	}

	fmt.Printf("setup complete: tenant=%s brand=%s user=%s platform_admin=true\n", *tenantSlug, *brandSlug, normalizedEmail)
	return nil
}

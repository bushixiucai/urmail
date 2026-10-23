package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/config"
)

func targetDatabaseURLs(cfg *config.Config, migrationURL string) (map[string]string, error) {
	target, err := url.Parse(migrationURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target migration URL")
	}
	values := map[string]string{"URMAIL_DATABASE_URL": cfg.DatabaseURL, "URMAIL_DATABASE_URL_PLATFORM": cfg.DatabaseURLPlatform, "URMAIL_DATABASE_URL_MIGRATE": cfg.DatabaseURLMigrate, "URMAIL_DATABASE_URL_DOVECOT": cfg.DatabaseURLDovecot}
	for key, value := range values {
		uri, err := url.Parse(value)
		if err != nil || uri.Scheme == "" || uri.User == nil {
			return nil, fmt.Errorf("target role credential %s is missing", key)
		}
		if uri.Host != target.Host || uri.Query().Get("host") != target.Query().Get("host") {
			return nil, fmt.Errorf("target role endpoints must match the selected restore cluster")
		}
		uri.Path = target.Path
		values[key] = uri.String()
	}
	values["URMAIL_DATABASE_URL_MIGRATE"] = migrationURL
	return values, nil
}
func mergeTargetDatabaseURLs(raw []byte, values map[string]string) []byte {
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		key, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if replacement, ok := values[strings.TrimSpace(key)]; ok {
			lines[i] = key + "=" + replacement
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
func preserveTargetDatabaseURLs(values map[string]string) error {
	for _, name := range []string{"/etc/urmail/urmaild.env", "/etc/urmail/agent.env"} {
		raw, err := os.ReadFile(name) // #nosec G304 -- iterates only the two fixed root-owned environment files.
		if err != nil {
			return err
		}
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(name), ".restore-env-")
		if err != nil {
			return err
		}
		temporary := file.Name()
		cleanup := func() { _ = file.Close(); _ = os.Remove(temporary) }
		if _, err = file.Write(mergeTargetDatabaseURLs(raw, values)); err != nil {
			cleanup()
			return err
		}
		if err = file.Chmod(info.Mode().Perm()); err != nil {
			cleanup()
			return err
		}
		// tar resolves stored user/group names on the target. Preserve that
		// ownership while atomically replacing only the database URL fields.
		if err = preserveFileOwnership(file, info); err != nil {
			cleanup()
			return err
		}
		if err = file.Sync(); err != nil {
			cleanup()
			return err
		}
		if err = file.Close(); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		if err = os.Rename(temporary, name); err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}

func verifyTargetRoleConnections(ctx context.Context, values map[string]string) error {
	expected := map[string]string{"URMAIL_DATABASE_URL": "urmail_app", "URMAIL_DATABASE_URL_PLATFORM": "urmail_platform", "URMAIL_DATABASE_URL_MIGRATE": "urmail_migrate", "URMAIL_DATABASE_URL_DOVECOT": "urmail_dovecot"}
	for key, role := range expected {
		conn, err := pgx.Connect(ctx, values[key])
		if err != nil {
			return fmt.Errorf("target %s authentication failed", role)
		}
		var actual string
		err = conn.QueryRow(ctx, "SELECT current_user").Scan(&actual)
		_ = conn.Close(ctx)
		if err != nil || actual != role {
			return fmt.Errorf("target %s role identity mismatch", role)
		}
	}
	return nil
}

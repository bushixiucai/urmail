package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// backupPeerDatabaseURL only selects the managed local PostgreSQL instance.
// BYPASSRLS is not inherited through membership, so migration credentials alone
// cannot dump FORCE-RLS tables. Root uses fixed local peer authentication.
func backupPeerDatabaseURL(connection string) (string, error) {
	if container := os.Getenv("URMAIL_BACKUP_POSTGRES_CONTAINER"); container != "" {
		return containerBackupSourceURL(connection, container)
	}
	u, err := url.Parse(connection)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() != "urmail_migrate" || u.Fragment != "" {
		return "", fmt.Errorf("backup requires the managed migration database URL")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
		return "", fmt.Errorf("backup requires the managed local PostgreSQL instance")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" || len(database) > 63 || strings.IndexFunc(database, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_", r)
	}) >= 0 {
		return "", fmt.Errorf("backup database name is not supported")
	}
	query := u.Query()
	for key := range query {
		if key != "sslmode" && key != "host" {
			return "", fmt.Errorf("backup connection overrides are not supported")
		}
	}
	if sockets, exists := query["host"]; exists && (len(sockets) != 1 || sockets[0] != "/var/run/postgresql") {
		return "", fmt.Errorf("backup socket is not managed")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if port != "5432" {
		return "", fmt.Errorf("backup requires the managed PostgreSQL port")
	}
	peer := &url.URL{Scheme: "postgresql", User: url.User("postgres"), Path: "/" + database}
	peer.RawQuery = url.Values{"host": {"/var/run/postgresql"}, "port": {port}, "sslmode": {"disable"}}.Encode()
	return peer.String(), nil
}

func backupDatabaseCommand(ctx context.Context, peerURL string) (*exec.Cmd, error) {
	if strings.HasPrefix(peerURL, "urmail-container-postgresql://") {
		return containerBackupDatabaseCommand(ctx, peerURL)
	}
	command, err := postgresCommand(ctx, "pg_dump", peerURL, "--no-owner", "--format=custom")
	if err != nil {
		return nil, err
	}
	args := append([]string{"-u", "postgres", "--", command.Path}, command.Args[1:]...)
	command = exec.CommandContext(ctx, "/usr/sbin/runuser", args...) // #nosec G204 G702 -- fixed root-only account/tool; managed peer URI and separate output path.
	// The postgres OS account must not inherit root-only offsite credentials.
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	return command, nil
}

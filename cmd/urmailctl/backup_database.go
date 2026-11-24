package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Managed compose topology (P1, P4-FB-02). A managed deployment declares it
// explicitly and names the compose PostgreSQL service; only the managed
// migration role is accepted, and pg_dump enters the BYPASSRLS platform role
// through a session SET ROLE (membership is enforced by PostgreSQL, so a role
// without membership fails closed).
const (
	managedTopologyEnv  = "URMAIL_BACKUP_MANAGED_TOPOLOGY"
	managedPGHostEnv    = "URMAIL_BACKUP_MANAGED_PG_HOST"
	managedPGPortEnv    = "URMAIL_BACKUP_MANAGED_PG_PORT"
	managedBackupScheme = "urmail-managed-postgresql"
	managedPGRole       = "urmail_platform"
)

var managedServiceName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// managedTopology describes the explicitly declared deployment shape.
// "compose" accepts one project service name on 5432; "loopback" accepts only
// a loopback literal on a controlled high port so a host systemd unit can
// reach the stack without touching another project's 5432.
type managedTopology struct {
	mode string
	host string
	port string
}

func loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func managedTopologyFromEnv() (managedTopology, bool, error) {
	raw := strings.TrimSpace(os.Getenv(managedTopologyEnv))
	if raw == "" {
		return managedTopology{}, false, nil
	}
	switch raw {
	case "compose":
		if strings.TrimSpace(os.Getenv(managedPGPortEnv)) != "" {
			return managedTopology{}, false, fmt.Errorf("%s is not accepted with the compose topology", managedPGPortEnv)
		}
		host := strings.TrimSpace(os.Getenv(managedPGHostEnv))
		if host == "" {
			host = "pg"
		}
		if !managedServiceName.MatchString(host) {
			return managedTopology{}, false, fmt.Errorf("invalid managed PostgreSQL service name")
		}
		return managedTopology{mode: "compose", host: host, port: "5432"}, true, nil
	case "loopback":
		if strings.TrimSpace(os.Getenv(managedPGHostEnv)) != "" {
			return managedTopology{}, false, fmt.Errorf("%s is not accepted with the loopback topology", managedPGHostEnv)
		}
		port := strings.TrimSpace(os.Getenv(managedPGPortEnv))
		number, err := strconv.Atoi(port)
		if err != nil || number < 1024 || number > 65535 {
			return managedTopology{}, false, fmt.Errorf("%s must be a port between 1024 and 65535", managedPGPortEnv)
		}
		return managedTopology{mode: "loopback", host: "127.0.0.1", port: strconv.Itoa(number)}, true, nil
	default:
		return managedTopology{}, false, fmt.Errorf("unsupported managed backup topology %q", raw)
	}
}

// managedBackupDatabaseURL validates a managed DSN: the topology's exact
// host and port, the managed migration role with its password, and the same
// database/query whitelist as the local path.
func managedBackupDatabaseURL(connection string, topology managedTopology) (string, error) {
	u, err := url.Parse(connection)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() != "urmail_migrate" || u.Fragment != "" {
		return "", fmt.Errorf("managed backup requires the managed migration database URL")
	}
	password, present := u.User.Password()
	if !present || password == "" {
		return "", fmt.Errorf("managed backup requires managed migration credentials")
	}
	host := u.Hostname()
	if topology.mode == "loopback" {
		if !loopbackHost(host) {
			return "", fmt.Errorf("loopback backup requires a loopback host")
		}
	} else if host != topology.host {
		return "", fmt.Errorf("managed backup requires the compose PostgreSQL service %q", topology.host)
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" || len(database) > 63 || strings.IndexFunc(database, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_", r)
	}) >= 0 {
		return "", fmt.Errorf("backup database name is not supported")
	}
	query := u.Query()
	for key, values := range query {
		if key != "sslmode" || len(values) != 1 {
			return "", fmt.Errorf("backup connection overrides are not supported")
		}
		switch values[0] {
		case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		default:
			return "", fmt.Errorf("backup sslmode is not supported")
		}
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if port != topology.port {
		return "", fmt.Errorf("backup requires the managed PostgreSQL port %s", topology.port)
	}
	plan := &url.URL{Scheme: managedBackupScheme, User: url.UserPassword("urmail_migrate", password), Host: net.JoinHostPort(host, topology.port), Path: "/" + database}
	if mode := query.Get("sslmode"); mode != "" {
		plan.RawQuery = url.Values{"sslmode": {mode}}.Encode()
	}
	return plan.String(), nil
}

// managedBackupDatabaseCommand runs pg_dump directly (no host postgres OS
// account exists in compose) with a restricted environment and a session
// SET ROLE into the BYPASSRLS platform role.
func managedBackupDatabaseCommand(ctx context.Context, plan string) (*exec.Cmd, error) {
	u, err := url.Parse(plan)
	if err != nil || u.Scheme != managedBackupScheme || u.User == nil || (!managedServiceName.MatchString(u.Hostname()) && !loopbackHost(u.Hostname())) {
		return nil, errors.New("invalid managed backup plan")
	}
	password, present := u.User.Password()
	if !present || password == "" {
		return nil, errors.New("managed backup plan lost its credentials")
	}
	u.Scheme = "postgresql"
	u.User = url.User("urmail_migrate")
	command := exec.CommandContext(ctx, "/usr/bin/pg_dump", "--no-owner", "--format=custom", "--dbname", u.String()) // #nosec G204 G702 -- fixed executable, validated managed service, credentials excluded from argv.
	command.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"PGPASSWORD=" + password,
		"PGOPTIONS=-c role=" + managedPGRole,
	}
	return command, nil
}

// backupPeerDatabaseURL only selects the managed local PostgreSQL instance.
// BYPASSRLS is not inherited through membership, so migration credentials alone
// cannot dump FORCE-RLS tables. Root uses fixed local peer authentication.
func backupPeerDatabaseURL(connection string) (string, error) {
	if container := os.Getenv("URMAIL_BACKUP_POSTGRES_CONTAINER"); container != "" {
		return containerBackupSourceURL(connection, container)
	}
	if topology, managed, topologyErr := managedTopologyFromEnv(); topologyErr != nil {
		return "", topologyErr
	} else if managed {
		return managedBackupDatabaseURL(connection, topology)
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
	if strings.HasPrefix(peerURL, managedBackupScheme+"://") {
		return managedBackupDatabaseCommand(ctx, peerURL)
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

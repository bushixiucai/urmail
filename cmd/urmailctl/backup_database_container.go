package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var backupContainerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var backupDatabaseName = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)
var backupContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The root-only selector preserves the current managed database. It never
// accepts a daemon-supplied container, shell command or remote database.
func containerBackupSourceURL(connection, container string) (string, error) {
	u, e := url.Parse(connection)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() != "urmail_migrate" || u.Fragment != "" || !backupContainerName.MatchString(container) {
		return "", errors.New("invalid managed container backup source")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return "", errors.New("container backup requires a loopback migration endpoint")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if !backupDatabaseName.MatchString(database) {
		return "", errors.New("invalid managed backup database name")
	}
	parameters, parseErr := url.ParseQuery(u.RawQuery)
	if parseErr != nil {
		return "", errors.New("invalid container backup connection parameters")
	}
	for key, values := range parameters {
		if key != "sslmode" || len(values) != 1 {
			return "", errors.New("container backup connection overrides are forbidden")
		}
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	number, e := strconv.Atoi(port)
	if e != nil || number < 1 || number > 65535 {
		return "", errors.New("invalid managed database port")
	}
	plan := url.URL{Scheme: "urmail-container-postgresql", Host: container, Path: "/" + database, RawQuery: url.Values{"host_port": {strconv.Itoa(number)}}.Encode()}
	return plan.String(), nil
}

type backupContainerInspection struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
	Ports   map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	} `json:"ports"`
}

func validatedBackupContainer(source *url.URL, raw []byte) (string, error) {
	if len(raw) > 64<<10 || source.Scheme != "urmail-container-postgresql" || source.User != nil || source.Fragment != "" || !backupContainerName.MatchString(source.Host) || !backupDatabaseName.MatchString(strings.TrimPrefix(source.Path, "/")) {
		return "", errors.New("invalid managed container backup plan")
	}
	query, parseErr := url.ParseQuery(source.RawQuery)
	if parseErr != nil {
		return "", errors.New("invalid managed container port plan")
	}
	port, e := strconv.Atoi(query.Get("host_port"))
	if e != nil || port < 1 || port > 65535 || len(query) != 1 || len(query["host_port"]) != 1 {
		return "", errors.New("invalid managed container port plan")
	}
	var inspected backupContainerInspection
	if json.Unmarshal(raw, &inspected) != nil || !inspected.Running || !backupContainerID.MatchString(inspected.ID) {
		return "", errors.New("managed PostgreSQL container is not running")
	}
	bound := false
	for _, binding := range inspected.Ports["5432/tcp"] {
		ip := net.ParseIP(binding.HostIP)
		if ip != nil && ip.IsLoopback() && binding.HostPort == strconv.Itoa(port) {
			bound = true
		}
	}
	if !bound {
		return "", errors.New("container is not the configured loopback PostgreSQL endpoint")
	}
	return inspected.ID, nil
}

func containerBackupDatabaseCommand(ctx context.Context, source string) (*exec.Cmd, error) {
	u, e := url.Parse(source)
	if e != nil || !backupContainerName.MatchString(u.Host) {
		return nil, errors.New("invalid managed container backup plan")
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	format := `{"id":{{json .Id}},"running":{{json .State.Running}},"ports":{{json .NetworkSettings.Ports}}}`
	// The projection deliberately excludes container environment and credentials.
	inspect := exec.CommandContext(inspectCtx, "/usr/bin/docker", "inspect", "--type", "container", "--format", format, u.Host) // #nosec G204 G702 -- fixed executable/arguments; root selector is a validated container identifier.
	raw, e := inspect.Output()
	if e != nil {
		return nil, errors.New("cannot inspect the managed PostgreSQL container")
	}
	id, e := validatedBackupContainer(u, raw)
	if e != nil {
		return nil, e
	}
	// Pin the immutable ID, not a name that could be replaced after inspection.
	command := exec.CommandContext(ctx, "/usr/bin/docker", "exec", "--user", "postgres", id, "pg_dump", "--host=/var/run/postgresql", "--port=5432", "--username=urmail_bootstrap", "--dbname="+strings.TrimPrefix(u.Path, "/"), "--no-owner", "--format=custom") // #nosec G204 G702 -- immutable validated container ID, fixed pg_dump, separately validated database identifier; no shell.
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	return command, nil
}

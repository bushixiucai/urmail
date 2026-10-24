package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// postgresCommand keeps passwords out of argv, where other local users can
// inspect them. The child receives credentials only through its private env.
func postgresCommand(ctx context.Context, program, connection string, args ...string) (*exec.Cmd, error) {
	executable := ""
	switch program {
	case "pg_dump":
		executable = "/usr/bin/pg_dump"
	case "pg_restore":
		executable = "/usr/bin/pg_restore"
	case "psql":
		executable = "/usr/bin/psql"
	default:
		return nil, fmt.Errorf("unsupported postgres utility")
	}
	uri, err := url.Parse(connection)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		return nil, fmt.Errorf("postgres utility requires a PostgreSQL URL")
	}
	password := os.Getenv("PGPASSWORD")
	if uri.User != nil {
		if supplied, present := uri.User.Password(); present {
			password = supplied
		}
		uri.User = url.User(uri.User.Username())
	}
	query := uri.Query()
	if query.Has("password") {
		password = query.Get("password")
	}
	query.Del("password")
	uri.RawQuery = query.Encode()
	cmd := exec.CommandContext(ctx, executable, append(args, "--dbname", uri.String())...) // #nosec G204 G702 -- executable is restricted above; arguments are separate and the credential is excluded.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PGPASSWORD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	return cmd, nil
}

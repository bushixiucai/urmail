// Package selfhost implements domain rules for selfhost.
package selfhost

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// ErrNotReady is the not ready error.
var ErrNotReady = errors.New("self-host setup is not ready")

// Input implements the corresponding domain rule.
type Input struct{ DatabaseURL, AdminEmail, Password, Hostname string }

// Preflight implements the corresponding domain rule.
func Preflight(in Input) error {
	databaseURL, err := url.Parse(in.DatabaseURL)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Hostname() == "" || strings.Trim(databaseURL.Path, "/") == "" || databaseURL.Fragment != "" {
		return ErrNotReady
	}
	if port := databaseURL.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ErrNotReady
		}
	}
	if !strings.Contains(in.AdminEmail, "@") || len(in.Password) < 12 || strings.TrimSpace(in.Hostname) == "" {
		return ErrNotReady
	}
	return nil
}

// Dial implements the corresponding domain rule.
type Dial func(context.Context, string) error

// PreflightConnected implements the corresponding domain rule.
func PreflightConnected(ctx context.Context, in Input, dial Dial) error {
	if err := Preflight(in); err != nil {
		return err
	}
	if dial == nil {
		return ErrNotReady
	}
	if err := dial(ctx, in.DatabaseURL); err != nil {
		return ErrNotReady
	}
	return nil
}

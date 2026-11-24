package sqlitedb

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"modernc.org/sqlite"
)

// currentSettingScalar implements PG's current_setting(name[, missing_ok])
// for the single-engine SQLite query surface.
//
// SQLite has no session variables: the ported SQL and triggers read
// current_setting() for the settings the PostgreSQL build publishes with
// set_config(). In this engine every setting is unbound, so the function
// returns NULL when missing_ok is true (the form every call site uses) and
// raises for the one-argument form, exactly like PostgreSQL.
//
// The consequence for the panel_credential_version trigger is fail-closed and
// stricter: current_setting('urmail.password_rehash',true) IS NOT 'true' holds,
// so every password_hash change bumps the credential generation. SQLite has no
// rehash path (PlatformRehashPanelPassword is the PostgreSQL set_config
// statement); the Go scope decorator, not SQL, carries the tenant/user checks.
func currentSettingScalar(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 && len(args) != 2 {
		return nil, fmt.Errorf("current_setting: want 1 or 2 arguments, got %d", len(args))
	}
	name, ok := args[0].(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("current_setting: setting name must be non-empty text")
	}
	missingOK := false
	if len(args) == 2 {
		switch v := args[1].(type) {
		case nil:
			missingOK = false
		case bool:
			missingOK = v
		case int64:
			missingOK = v != 0
		case string:
			missingOK = strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "on")
		default:
			return nil, fmt.Errorf("current_setting: missing_ok must be boolean")
		}
	}
	if !missingOK {
		return nil, fmt.Errorf("unrecognized configuration parameter %q", name)
	}
	return nil, nil
}
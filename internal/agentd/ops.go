package agentd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Whitelists for the M1 operations. Every value that reaches the filesystem
// or a command line passes through one of these.

// allowedWriteRoots lists the directories the agent may write into.
var allowedWriteRoots = []string{
	"/etc/urmail",
	"/etc/postfix/urmail",
	"/etc/dovecot/conf.d/urmail",
	"/etc/nginx/conf.d/urmail",
	"/etc/nsd/nsd.conf.d",
	"/etc/nsd/zones",
	"/var/lib/urmail",
}

// allowedUnits lists the systemd units the agent may restart.
var allowedUnits = map[string]struct{}{
	"postfix":       {},
	"dovecot":       {},
	"nginx":         {},
	"nsd":           {},
	"urmaild":       {},
	"urmaild-agent": {},
}

// postfixKeyPattern matches a Postfix configuration parameter name.
var postfixKeyPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// postfixValuePattern rejects values that could smuggle a second directive
// or shell metacharacters into main.cf.
var postfixValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.,:/@=+ -]*$`)

// WriteFileParams are the parameters of the file.write operation.
type WriteFileParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

// WriteFile writes a file inside an allowed directory.
func WriteFile(params json.RawMessage) (string, string, error) {
	var args WriteFileParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}

	cleaned, err := safePath(args.Path)
	if err != nil {
		return "", "", err
	}

	mode := os.FileMode(0o644)
	if args.Mode != "" {
		parsed, err := parseOctal(args.Mode)
		if err != nil {
			return "", "", fmt.Errorf("%w: %v", ErrRejected, err)
		}
		mode = parsed
	}

	if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if err := os.WriteFile(cleaned, []byte(args.Content), mode); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrFailed, err)
	}
	return cleaned, "", nil
}

// RestartUnitParams are the parameters of the systemctl.restart operation.
type RestartUnitParams struct {
	Unit string `json:"unit"`
}

// RestartUnit restarts a whitelisted systemd unit.
func RestartUnit(params json.RawMessage) (string, string, error) {
	var args RestartUnitParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if err := ValidateUnit(args.Unit); err != nil {
		return "", "", err
	}

	cmd := exec.Command("systemctl", "restart", args.Unit)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: systemctl restart %s: %v", ErrFailed, args.Unit, err)
	}
	return string(output), "", nil
}

// ValidateUnit checks that a systemd unit may be restarted.
func ValidateUnit(unit string) error {
	if _, ok := allowedUnits[unit]; !ok {
		return fmt.Errorf("%w: unit %q is not allowed", ErrRejected, unit)
	}
	return nil
}

// PostconfSetParams are the parameters of the postconf.set operation.
type PostconfSetParams struct {
	Keys map[string]string `json:"keys"`
}

// PostconfSet applies Postfix configuration keys with postconf.
func PostconfSet(params json.RawMessage) (string, string, error) {
	var args PostconfSetParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrBadRequest, err)
	}

	arguments, err := BuildPostconfArgs(args.Keys)
	if err != nil {
		return "", "", err
	}

	cmd := exec.Command("postconf", arguments...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: postconf: %v", ErrFailed, err)
	}
	return string(output), "", nil
}

// BuildPostconfArgs validates a set of Postfix parameters and renders the
// argument list for postconf.
//
// Validation is separated from execution so that it can be tested without
// running postconf against the real system.
func BuildPostconfArgs(keys map[string]string) ([]string, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no keys given", ErrRejected)
	}

	arguments := []string{"-e"}
	for key, value := range keys {
		if !postfixKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: key %q is not allowed", ErrRejected, key)
		}
		if !postfixValuePattern.MatchString(value) {
			return nil, fmt.Errorf("%w: value for %q contains forbidden characters", ErrRejected, key)
		}
		arguments = append(arguments, key+"="+value)
	}
	return arguments, nil
}

// safePath resolves a path and verifies that it stays inside an allowed root.
// It rejects traversal attempts and symlink escapes.
func safePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrRejected)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path %q must be absolute", ErrRejected, path)
	}

	cleaned := filepath.Clean(path)
	for _, root := range allowedWriteRoots {
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return cleaned, nil
		}
	}
	return "", fmt.Errorf("%w: path %q is outside the allowed roots", ErrRejected, path)
}

// parseOctal parses an octal file mode string.
func parseOctal(value string) (os.FileMode, error) {
	var mode uint32
	if _, err := fmt.Sscanf(value, "%o", &mode); err != nil {
		return 0, fmt.Errorf("%w: invalid mode %q", ErrRejected, value)
	}
	if mode > 0o7777 {
		return 0, fmt.Errorf("%w: mode %q out of range", ErrRejected, value)
	}
	return os.FileMode(mode), nil
}

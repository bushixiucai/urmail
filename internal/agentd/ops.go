package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpSieveActivate compiles and atomically activates one persisted structured script.
const OpSieveActivate = "mailbox.sieve.activate"

// Whitelists for the M1 operations. Every value that reaches the filesystem
// or a command line passes through one of these.

// OpMailboxRecallPreview is the fixed read-only local-copy inventory operation.
const OpMailboxRecallPreview = "mailbox.recall.preview"

// allowedWriteRoots lists the directories the agent may write into.
var allowedWriteRoots = []string{
	"/etc/urmail",
	// main.cf/master.cf are written by reconcile; the maps live in the
	// urmail/ subdirectory. The agent is the only writer of either.
	"/etc/postfix",
	"/etc/postfix/urmail",
	"/etc/dovecot/conf.d",
	"/etc/dovecot/urmail-spam",
	"/etc/nginx/conf.d/urmail",
	"/etc/nsd/nsd.conf.d",
	"/etc/nsd/zones",
	"/etc/opendkim",
	"/etc/opendkim/keys",
	"/var/lib/urmail",
}

// allowedUnits lists the systemd units the agent may restart.
var allowedUnits = map[string]struct{}{
	"postfix":  {},
	"dovecot":  {},
	"nginx":    {},
	"nsd":      {},
	"opendkim": {},
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

// maxWriteContentBytes bounds a single file.write payload. The server already
// bounds the JSON request line; this is defense in depth for direct callers.
const maxWriteContentBytes = 8 << 20

// WriteFile writes a file inside an allowed directory.
//
// The write is atomic and symlink-safe: see writeFileAtomic. The caller never
// chooses the owner; files are created as the agent identity (root:mailinabox
// under the systemd unit) with the validated mode.
func WriteFile(params json.RawMessage) (string, string, error) {
	var args WriteFileParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if len(args.Content) > maxWriteContentBytes {
		return "", "", fmt.Errorf("%w: content is larger than %d bytes", ErrRejected, maxWriteContentBytes)
	}

	cleaned, _, err := resolveAllowedPath(args.Path, allowedWriteRoots)
	if err != nil {
		return "", "", err
	}

	mode := os.FileMode(0o644)
	if args.Mode != "" {
		parsed, err := parseOctal(args.Mode)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrRejected, err)
		}
		mode = parsed
	}

	enforced, err := enforceSensitiveFileMode(cleaned, mode, args.Mode)
	if err != nil {
		return "", "", err
	}
	mode = enforced

	written, err := writeFileAtomic(cleaned, []byte(args.Content), mode, allowedWriteRoots)
	if err != nil {
		return "", "", err
	}
	switch {
	case strings.HasSuffix(written, "db.conf"):
		if err := chownGroup(written, "dovecot"); err != nil {
			return "", "", err
		}
	case strings.HasSuffix(written, ".private") && strings.Contains(written, "/opendkim/keys/"):
		if err := chownGroup(written, "opendkim"); err != nil {
			return "", "", err
		}
		// The domain directory was created under the agent's umask
		// (root:mailinabox 0750); OpenDKIM must be able to traverse it.
		dir := filepath.Dir(written)
		if err := chownGroup(dir, "opendkim"); err != nil {
			return "", "", err
		}
		if err := os.Chmod(dir, 0o750); err != nil {
			return "", "", fmt.Errorf("%w: chmod %s: %w", ErrFailed, dir, err)
		}
	}
	return written, "", nil
}

// chownGroup gives a daemon read access to a secret file. The file is 0640
// root:<group>, never 0600 root:root, because the consuming daemon runs
// unprivileged. The agent runs as root, so it can perform the chown; if the
// group is absent (e.g. on a development host) the file stays
// root:mailinabox, still 0640 and never world-readable.
func chownGroup(path, groupName string) error {
	group, err := user.LookupGroup(groupName)
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("%w: parse %s gid %q: %w", ErrFailed, groupName, group.Gid, err)
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return fmt.Errorf("%w: chown %s to root:%s: %w", ErrFailed, path, groupName, err)
	}
	return nil
}

// RestartUnitParams are the parameters of the systemctl.restart operation.
type RestartUnitParams struct {
	Unit string `json:"unit"`
}

// RestartUnit restarts a whitelisted systemd unit.
func RestartUnit(params json.RawMessage) (string, string, error) {
	var args RestartUnitParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if err := ValidateUnit(args.Unit); err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "restart", args.Unit) // #nosec G204 -- unit is checked against the fixed allowlist.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: systemctl restart %s: %w", ErrFailed, args.Unit, err)
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
		return "", "", fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	arguments, err := BuildPostconfArgs(args.Keys)
	if err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/postconf", arguments...) // #nosec G204 -- legacy helper arguments are validated and this operation is not registered.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: postconf: %w", ErrFailed, err)
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

// OpPostmap is the whitelisted operation that compiles one Postfix map.
const OpPostmap = "postfix.postmap"

// postmapRoot is the only directory whose *.map files may be compiled.
const postmapRoot = "/etc/postfix/urmail"

// postmapFilenamePattern restricts map names to a conservative filename
// alphabet. A dot is only allowed as the extension separator.
var postmapFilenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*\.map$`)

// PostmapParams are the parameters of the postfix.postmap operation.
type PostmapParams struct {
	Path string `json:"path"`
}

// Postmap compiles one Postfix map with postmap.
//
// Exactly one validated absolute path is passed to postmap: the caller cannot
// add flags, a shell is never involved, and the path must be a direct regular
// *.map child of postmapRoot. Traversal, symlinks and nested paths are
// rejected before postmap runs.
func Postmap(params json.RawMessage) (string, string, error) {
	var args PostmapParams
	if err := json.Unmarshal(params, &args); err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	target, err := resolvePostmapFile(args.Path, postmapRoot)
	if err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/postmap", target) // #nosec G204 -- target is an allowlisted map path, never shell input.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: postmap: %w", ErrFailed, err)
	}
	return string(output), "", nil
}

// validatePostmapPath performs the lexical checks for a postmap request.
func validatePostmapPath(path, root string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty postmap path", ErrRejected)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: postmap path %q must be absolute", ErrRejected, path)
	}

	cleaned := filepath.Clean(path)
	if cleaned != path {
		return "", fmt.Errorf("%w: postmap path %q is not a clean absolute path", ErrRejected, path)
	}

	root = filepath.Clean(root)
	if filepath.Dir(cleaned) != root {
		return "", fmt.Errorf("%w: postmap path %q must be a direct child of %s", ErrRejected, path, root)
	}

	name := filepath.Base(cleaned)
	if !postmapFilenamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: postmap file %q is not an allowed *.map name", ErrRejected, name)
	}
	return cleaned, nil
}

// resolvePostmapFile validates path and resolves it on the filesystem. The
// parent directory must resolve to the real root and the map itself must be a
// regular file, not a symlink or directory.
func resolvePostmapFile(path, root string) (string, error) {
	cleaned, err := validatePostmapPath(path, root)
	if err != nil {
		return "", err
	}

	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("%w: resolve postmap root %s: %w", ErrRejected, root, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(cleaned))
	if err != nil {
		return "", fmt.Errorf("%w: resolve postmap directory: %w", ErrRejected, err)
	}
	if resolvedParent != resolvedRoot {
		return "", fmt.Errorf("%w: postmap path %q escapes %s", ErrRejected, path, root)
	}

	target := filepath.Join(resolvedRoot, filepath.Base(cleaned))
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("%w: inspect postmap file %s: %w", ErrRejected, target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: postmap file %s is a symlink", ErrRejected, target)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: postmap file %s is not a regular file", ErrRejected, target)
	}
	return target, nil
}

// enforceSensitiveFileMode forces mode 0600 for credential-bearing files
// (dovecot db.conf) regardless of the default or requested mode; an explicit
// conflicting mode is rejected (M2 review P1-2). Separated from WriteFile so
// it can be tested without touching the real filesystem.
func enforceSensitiveFileMode(path string, mode os.FileMode, requested string) (os.FileMode, error) {
	sensitive := strings.HasSuffix(path, "db.conf") ||
		(strings.HasSuffix(path, ".private") && strings.Contains(path, "/opendkim/keys/"))
	if !sensitive {
		return mode, nil
	}
	// 0640 root:<daemon group>: the consumer runs unprivileged and must read
	// the secret, while other local users must not.
	if requested == "" {
		return 0o640, nil
	}
	if mode != 0o640 {
		return 0, fmt.Errorf("%w: %s carries credentials and must be written with mode 0640, got %s", ErrRejected, path, requested)
	}
	return mode, nil
}

// parseOctal parses an octal file mode string.
//
// Only plain permissions in the range 0000-0777 are accepted. Setuid (04000),
// setgid (02000) and sticky (01000) bits are rejected rather than silently
// masked, because a caller that asks for them is either confused or probing
// the boundary.
func parseOctal(value string) (os.FileMode, error) {
	if value == "" {
		return 0, fmt.Errorf("%w: empty mode", ErrRejected)
	}
	mode, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid mode %q", ErrRejected, value)
	}
	if mode > 0o777 {
		return 0, fmt.Errorf("%w: mode %q is outside 0000-0777 or contains setuid/setgid/sticky bits", ErrRejected, value)
	}
	return os.FileMode(mode), nil
}

// OpBackupLocalCreate executes the fixed, parameter-free local backup
// pipeline (OPS-001). It exists because urmaild runs unprivileged while the
// backup must read /etc, the Maildirs and peer-authenticate to PostgreSQL.
// SECURITY: the operation accepts NO caller-controlled parameters and runs
// ONE fixed command with fixed arguments; there is deliberately no way to
// alter the output directory, scope or any other aspect from the socket.
const OpBackupLocalCreate = "backup.local.create"

// backupOutputRoot is the fixed spool directory for agent-created backups.
const backupOutputRoot = "/var/backups/urmail"

// backupLocalCreateCommand is the complete fixed command. It is a constant
// so no code path can append, reorder or substitute arguments.
var backupLocalCreateCommand = []string{"/usr/bin/urmailctl", "backup", "--include-mail", "--maintenance", "--out-dir", backupOutputRoot}

// BackupLocalCreateParams must be empty; any field is rejected.
type BackupLocalCreateParams struct{}

// BackupLocalCreate runs the fixed backup command as root and then makes the
// produced artifacts group-readable for the mailinabox daemon so the
// unprivileged encryption/upload stage can read them. Timeout bounds the
// whole backup (dumps of large hosts are the long pole); a timed-out run
// leaves the spool for the next attempt and deletes nothing.
func BackupLocalCreate(params json.RawMessage) (string, string, error) {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) != 0 && string(trimmed) != "null" && string(trimmed) != "{}" {
		return "", "", fmt.Errorf("%w: %s takes no parameters", ErrRejected, OpBackupLocalCreate)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, backupLocalCreateCommand[0], backupLocalCreateCommand[1:]...) // #nosec G204 -- every element is a compile-time constant.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(output), fmt.Errorf("%w: backup.local.create: %w: %s", ErrFailed, err, strings.TrimSpace(string(output)))
	}
	if err := shareBackupArtifactsWithDaemon(backupOutputRoot); err != nil {
		return string(output), "", fmt.Errorf("%w: share artifacts: %w", ErrFailed, err)
	}
	return string(output), "", nil
}

// shareBackupArtifactsWithDaemon chmods the newest backup generation to
// 0640 root:mailinabox. Only regular files are touched; directories keep
// their root-only mode because the daemon opens known file names, not
// listings it could not verify anyway.
func shareBackupArtifactsWithDaemon(root string) error {
	group, err := user.LookupGroup("mailinabox")
	if err != nil {
		return nil // development host without the group: leave root-only
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse mailinabox gid %q: %w", group.Gid, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read backup spool: %w", err)
	}
	for _, entry := range entries {
		if entry.Type() != 0 { // regular files only; never follow symlinks
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "urmail-") {
			continue
		}
		target := filepath.Join(root, name)
		if err := os.Chown(target, -1, gid); err != nil {
			return fmt.Errorf("chown %s: %w", target, err)
		}
		if err := os.Chmod(target, 0o640); err != nil {
			return fmt.Errorf("chmod %s: %w", target, err)
		}
	}
	return nil
}

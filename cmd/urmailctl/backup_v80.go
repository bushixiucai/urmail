package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func selectedFileProfile(args []string) (string, bool, error) {
	profile := ""
	found := false
	for i := 0; i < len(args); i++ {
		value := ""
		switch {
		case args[i] == "--profile":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", true, errors.New("profile name is required")
			}
			i++
			value = args[i]
		case strings.HasPrefix(args[i], "--profile="):
			value = strings.TrimPrefix(args[i], "--profile=")
		default:
			continue
		}
		if found {
			return "", true, errors.New("duplicate profile selection")
		}
		found = true
		profile = value
	}
	if !found {
		for _, arg := range args {
			for _, only := range []string{"--source-tree", "--preview", "--apply", "--offline-source-confirmed", "--expected-preview"} {
				if arg == only || strings.HasPrefix(arg, only+"=") {
					return "", false, errors.New("file-profile options require explicit --profile v80-preserve")
				}
			}
		}
	}
	if found && profile != v80Profile {
		return "", true, errors.New("unsupported file profile")
	}
	return profile, found, nil
}

func runV80Backup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("backup v80-preserve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit fixed legacy file profile")
	base := flags.String("source-tree", "/", "offline logical root tree; fixed profile paths only")
	out := flags.String("out-dir", "/var/backups/urmail", "private artifact destination")
	preview := flags.Bool("preview", false, "read-only layout preview")
	apply := flags.Bool("apply", false, "create a file bundle only after explicit offline confirmation")
	maintenance := flags.Bool("maintenance", false, "confirm an approved maintenance window")
	includeMail := flags.Bool("include-mail", false, "explicitly include persistent mailbox and groupware bytes")
	offline := flags.Bool("offline-source-confirmed", false, "confirm all source writers and independent jobs are stopped")
	expected := flags.String("expected-preview", "", "exact reviewed layout SHA256")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *profile != v80Profile || (*preview && *apply) {
		return errors.New("invalid v80-preserve backup arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("v80-preserve backup requires a root operator")
	}
	if *apply && (!*maintenance || !*includeMail || !*offline || len(*expected) != 64) {
		return errors.New("file bundle creation requires --maintenance --include-mail --offline-source-confirmed and --expected-preview SHA256")
	}
	absolute, err := filepath.Abs(*base)
	if err != nil {
		return errors.New("invalid profile source tree")
	}
	if err = validatePrivateBackupDirectory(absolute); err != nil {
		return errors.New("profile source tree must have root-controlled ancestry")
	}
	live, err := v80LiveRoot(absolute)
	if err != nil {
		return err
	}
	if *apply && live {
		if err = checkV80WritersStopped(ctx); err != nil {
			return err
		}
	}
	inventory, err := collectV80Inventory(absolute)
	if err != nil {
		return err
	}
	report := map[string]any{"profile": v80Profile, "scope": "legacy-file-profile", "changed": false, "status": "preview_ready", "layout_preview_sha256": inventory.LayoutSHA256, "member_count": len(inventory.Members), "regular_bytes": inventory.Bytes, "excluded_runtime_links": len(inventory.Excluded), "blockers": inventory.Blockers, "offline_required_for_bundle": true, "live_source": live, "restore_apply_available": false, "required_follow_up": v80FollowUp}
	if len(inventory.Blockers) > 0 {
		report["status"] = "blocked"
		if err = json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
		return errors.New("legacy profile layout requires review; no bundle created")
	}
	if !*apply {
		return json.NewEncoder(output).Encode(report)
	}
	if inventory.LayoutSHA256 != *expected {
		return errors.New("legacy source layout changed since preview; no bundle created")
	}
	outAbsolute, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	for _, root := range v80Roots {
		selected := filepath.Join(absolute, filepath.FromSlash(root.Path))
		if outAbsolute == selected || strings.HasPrefix(outAbsolute, selected+string(os.PathSeparator)) {
			return errors.New("bundle destination must be outside all source profile roots")
		}
	}
	ancestor := outAbsolute
	for {
		_, statErr := os.Lstat(ancestor)
		if statErr == nil {
			break
		}
		if !os.IsNotExist(statErr) || ancestor == filepath.Dir(ancestor) {
			return errors.New("bundle destination ancestry cannot be checked")
		}
		ancestor = filepath.Dir(ancestor)
	}
	if err = validatePrivateBackupDirectory(ancestor); err != nil {
		return errors.New("bundle destination ancestry is not root-controlled")
	}
	if err = os.MkdirAll(*out, 0o700); err != nil {
		return errors.New("private bundle destination cannot be created")
	}
	if err = validatePrivateBackupDirectory(*out); err != nil {
		return errors.New("bundle destination is not root-private")
	}
	if err = os.Chmod(*out, 0o700); err != nil {
		return err
	}
	manifestPath, err := createV80Bundle(ctx, absolute, *out, inventory, live)
	if err != nil {
		return errors.New("legacy file bundle creation failed; inspect private partial artifacts; source services were not changed")
	}
	report["changed"] = true
	report["status"] = "file_bundle_created_restore_still_blocked"
	report["manifest"] = manifestPath
	return json.NewEncoder(output).Encode(report)
}
func v80LiveRoot(base string) (bool, error) {
	a, err := os.Stat(base)
	if err != nil {
		return false, errors.New("source tree cannot be identified")
	}
	b, err := os.Stat("/")
	if err != nil {
		return false, err
	}
	return os.SameFile(a, b), nil
}

var v80WriterUnits = []string{"cron.service", "mailinabox.service", "nginx.service", "postfix.service", "dovecot.service", "certbot.timer", "certbot.service", "php8.1-fpm.service", "php8.2-fpm.service", "php8.3-fpm.service", "php8.4-fpm.service", "php8.5-fpm.service", "php8.6-fpm.service"}

func checkV80WritersStopped(ctx context.Context) error {
	return verifyV80WriterStates(func(unit string) (string, error) {
		raw, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=LoadState,ActiveState", unit).Output() // #nosec G204 G702 -- fixed read-only probe and unit allowlist, never a stop/start command.
		return string(raw), err
	})
}
func verifyV80WriterStates(probe func(string) (string, error)) error {
	for _, unit := range v80WriterUnits {
		raw, err := probe(unit)
		if err != nil {
			return errors.New("source writer state cannot be proved inactive")
		}
		states := map[string]string{}
		for _, line := range strings.Split(raw, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				states[key] = value
			}
		}
		required := unit == "cron.service" || unit == "mailinabox.service" || unit == "nginx.service" || unit == "postfix.service" || unit == "dovecot.service"
		if required && states["LoadState"] != "loaded" {
			return errors.New("required legacy writer unit cannot be identified")
		}
		if states["ActiveState"] != "inactive" && states["ActiveState"] != "failed" {
			return errors.New("live source writers require explicit external quiescence; preview only")
		}
	}
	return nil
}

type v80ContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r v80ContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func createV80Bundle(ctx context.Context, base, out string, inventory v80Inventory, live bool) (string, error) {
	if len(inventory.Blockers) > 0 {
		return "", errors.New("blocked profile cannot be archived")
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	archiveName := "urmail-v80-preserve-" + stamp + ".tar.gz"
	archivePath := filepath.Join(out, archiveName)
	partial := archivePath + ".partial"
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 G703 -- root-selected private artifact directory; never a source path.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	archiveHash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(file, archiveHash))
	tw := tar.NewWriter(gz)
	members := append([]v80Member(nil), inventory.Members...)
	for i := range members {
		member := &members[i]
		header := &tar.Header{Name: member.Name, Mode: member.Mode, Uid: member.UID, Gid: member.GID, Size: member.Size, ModTime: member.ModifiedAt, Linkname: member.LinkTarget, Format: tar.FormatPAX}
		switch member.Kind {
		case "file":
			header.Typeflag = tar.TypeReg
		case "directory":
			header.Typeflag = tar.TypeDir
		case "symlink":
			header.Typeflag = tar.TypeSymlink
		default:
			return "", errors.New("unsupported profile member")
		}
		if err = tw.WriteHeader(header); err != nil {
			return "", err
		}
		if member.Kind == "file" {
			source, sourceErr := openV80Member(base, *member)
			if sourceErr != nil {
				return "", sourceErr
			}
			contentHash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(tw, contentHash), v80ContextReader{ctx, source})
			after, statErr := source.Stat()
			closeErr := source.Close()
			if copyErr != nil || statErr != nil || closeErr != nil || n != member.Size || !v80SameInfo(member.info, after) {
				return "", errors.New("source changed while archiving")
			}
			member.SHA256 = hex.EncodeToString(contentHash.Sum(nil))
		} else {
			member.SHA256 = v80MemberMarker(*member)
		}
	}
	if err = tw.Close(); err != nil {
		return "", err
	}
	if err = gz.Close(); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = verifyV80InventoryStable(base, inventory.Members); err != nil {
		return "", err
	}
	if live {
		if err = checkV80WritersStopped(ctx); err != nil {
			return "", err
		}
	}
	if err = os.Rename(partial, archivePath); err != nil {
		return "", err
	}
	manifest := v80Manifest{Version: 2, Profile: v80Profile, ProfileVersion: 1, SpecSHA256: v80SpecDigest(), Scope: "legacy-file-profile", CreatedAt: time.Now().UTC(), OfflineConfirmed: true, LiveWriterCheck: live, LayoutSHA256: inventory.LayoutSHA256, Artifact: backupArtifact{Name: archiveName, SHA256: hex.EncodeToString(archiveHash.Sum(nil)), Size: info.Size(), Mode: 0o600}, Members: members, Excluded: inventory.Excluded, RequiredFollowUp: v80FollowUp}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if len(raw) > 128<<20 {
		return "", errors.New("profile manifest exceeds its supported size")
	}
	manifestPath := filepath.Join(out, "urmail-v80-preserve-"+stamp+".manifest.json")
	manifestFile, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 G703 -- private root-authorized manifest, containing source paths but no public stdout.
	if err != nil {
		return "", err
	}
	defer func() { _ = manifestFile.Close() }()
	if _, err = manifestFile.Write(raw); err != nil {
		return "", err
	}
	if err = manifestFile.Sync(); err != nil {
		return "", err
	}
	if err = manifestFile.Close(); err != nil {
		return "", err
	}
	directory, err := os.Open(out) // #nosec G304 G703 -- syncs only the explicit private artifact directory, never a source path.
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	if err = directory.Sync(); err != nil {
		return "", err
	}
	return manifestPath, nil
}

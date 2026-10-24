package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runV80Restore(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore v80-preserve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit legacy file profile")
	manifestPath := flags.String("manifest", "", "private profile manifest")
	archivePath := flags.String("archive", "", "profile file bundle")
	_ = flags.Bool("preview", false, "validate only; no files, sessions, secrets or services are restored")
	apply := flags.Bool("apply", false, "unavailable until complete controlled recovery is implemented")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *profile != v80Profile {
		return errors.New("invalid legacy restore preview arguments")
	}
	if *apply {
		return errors.New("v80 profile restore apply is unavailable: writer fencing, same-PG owner restore, session invalidation, credential rebind and device reconciliation are required")
	}
	if os.Geteuid() != 0 {
		return errors.New("legacy profile preview requires a root operator")
	}
	if *manifestPath == "" || *archivePath == "" {
		return errors.New("legacy restore preview requires --manifest and --archive")
	}
	manifest, err := verifyV80Bundle(ctx, *manifestPath, *archivePath)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"profile": v80Profile, "status": "verified_file_profile_preview_only", "changed": false, "restore_apply_available": false, "cutover_ready": false, "member_count": len(manifest.Members), "excluded_runtime_links": len(manifest.Excluded), "required_follow_up": v80FollowUp, "quarantine_or_holds_released": false, "source_secrets_or_sessions_activated": false})
}

func rejectV80BundleAsDatabase(filename string) error {
	file, err := os.Open(filename) // #nosec G304 G703 -- explicit existing root-selected database artifact; read-only type preflight.
	if err != nil {
		return errors.New("database artifact cannot be read before restore")
	}
	defer func() { _ = file.Close() }()
	var magic [2]byte
	if _, err = io.ReadFull(file, magic[:]); err != nil {
		return errors.New("database artifact is truncated")
	}
	if magic == [2]byte{0x1f, 0x8b} {
		return errors.New("gzip file bundles are not PostgreSQL dumps; use explicit v80-preserve restore preview")
	}
	return nil
}

type v80Capture struct {
	bytes.Buffer
	limit int
}

func (c *v80Capture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := c.limit - c.Len()
	if remaining > 0 {
		_, _ = c.Buffer.Write(p[:min(remaining, n)])
	}
	return n, nil
}

func verifyV80Bundle(ctx context.Context, manifestPath, archivePath string) (v80Manifest, error) {
	var manifest v80Manifest
	file, err := os.Open(manifestPath) // #nosec G304 G703 -- root selects a private manifest; never executed.
	if err != nil {
		return manifest, errors.New("profile manifest cannot be opened")
	}
	defer func() { _ = file.Close() }()
	manifestInfo, statErr := file.Stat()
	if statErr != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 128<<20 {
		return manifest, errors.New("profile manifest exceeds its file bound")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 128<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, errors.New("profile manifest is invalid or exceeds its bound")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return manifest, errors.New("profile manifest has trailing data")
	}
	if manifest.Version != 2 || manifest.Profile != v80Profile || manifest.ProfileVersion != 1 || manifest.SpecSHA256 != v80SpecDigest() || manifest.Scope != "legacy-file-profile" || !manifest.OfflineConfirmed || manifest.RestoreApplyAvailable || manifest.SQLiteIntegrityVerified {
		return manifest, errors.New("profile identity or safety scope is unsupported")
	}
	if err = validateV80Layout(manifest.Members); err != nil {
		return manifest, err
	}
	if manifest.LayoutSHA256 != v80LayoutDigest(manifest.Members, manifest.Excluded) {
		return manifest, errors.New("profile layout digest mismatch")
	}
	for _, excluded := range manifest.Excluded {
		if excluded.Reason != "nonpersistent_roundcube_runtime" || !v80RuntimeLink(excluded.Name, excluded.Target) {
			return manifest, errors.New("profile exclusion is not a permitted runtime link")
		}
	}
	if manifest.Artifact.Name != filepath.Base(archivePath) || manifest.Artifact.Name != filepath.Base(manifest.Artifact.Name) || manifest.Artifact.Mode != 0o600 {
		return manifest, errors.New("profile artifact identity is invalid")
	}
	info, err := os.Lstat(archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != manifest.Artifact.Size {
		return manifest, errors.New("profile artifact metadata mismatch")
	}
	hash, err := fileSHA256(archivePath)
	if err != nil || hash != manifest.Artifact.SHA256 {
		return manifest, errors.New("profile artifact hash mismatch")
	}
	archive, err := os.Open(archivePath) // #nosec G304 G703 -- archive hash and exact manifest identity were checked; this is read-only validation, never extraction.
	if err != nil {
		return manifest, errors.New("profile archive cannot be read")
	}
	defer func() { _ = archive.Close() }()
	opened, err := archive.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return manifest, errors.New("profile artifact identity changed")
	}
	buffered := bufio.NewReader(archive)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return manifest, errors.New("profile archive is not a gzip stream")
	}
	defer func() { _ = gz.Close() }()
	gz.Multistream(false)
	reader := tar.NewReader(v80ContextReader{ctx, gz})
	expected := map[string]v80Member{}
	for _, member := range manifest.Members {
		expected[member.Name] = member
	}
	seen := map[string]bool{}
	configs := map[string][]byte{}
	fpmFound := false
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return manifest, errors.New("profile archive is truncated or invalid")
		}
		member, ok := expected[header.Name]
		if !ok || seen[header.Name] || !v80Allowed(header.Name) {
			return manifest, errors.New("profile archive contains an unbound or duplicate path")
		}
		seen[header.Name] = true
		kind := ""
		switch header.Typeflag {
		case tar.TypeReg:
			kind = "file"
		case tar.TypeDir:
			kind = "directory"
		case tar.TypeSymlink:
			kind = "symlink"
		default:
			return manifest, errors.New("profile archive contains an unsupported object")
		}
		if header.Uname != "" || header.Gname != "" {
			return manifest, errors.New("profile archive has unsupported name-based ownership")
		}
		for key := range header.PAXRecords {
			switch key {
			case "mtime", "path", "linkpath", "uid", "gid":
			default:
				return manifest, errors.New("profile archive has unsupported extension metadata")
			}
		}
		if kind != member.Kind || header.Mode != member.Mode || header.Uid != member.UID || header.Gid != member.GID || header.Size != member.Size || !header.ModTime.Equal(member.ModifiedAt) || header.Linkname != member.LinkTarget {
			return manifest, errors.New("profile archive member metadata mismatch")
		}
		if kind == "file" {
			contentHash := sha256.New()
			capture := &v80Capture{}
			if v80ConfigurationMember(member.Name) {
				capture.limit = 1 << 20
				if member.Name == "home/user-data/mail/users.sqlite" || member.Name == "home/user-data/mail/roundcube/roundcube.sqlite" || member.Name == "home/user-data/owncloud/owncloud.db" {
					capture.limit = 16
				} else if member.Size > int64(capture.limit) {
					return manifest, errors.New("profile configuration exceeds its bound")
				}
			}
			n, copyErr := io.CopyN(io.MultiWriter(contentHash, capture), reader, member.Size)
			if copyErr != nil || n != member.Size || hex.EncodeToString(contentHash.Sum(nil)) != member.SHA256 {
				return manifest, errors.New("profile member content hash mismatch")
			}
			if capture.limit > 0 {
				configs[member.Name] = capture.Bytes()
				if strings.HasPrefix(member.Name, "etc/php/") && v80FPMPattern.Match(capture.Bytes()) {
					fpmFound = true
				}
			}
		} else if member.SHA256 != v80MemberMarker(member) {
			return manifest, errors.New("profile non-file binding mismatch")
		}
	}
	if len(seen) != len(expected) {
		return manifest, errors.New("profile archive is missing required members")
	}
	if err = validateV80ConfigMaterial(configs, fpmFound); err != nil {
		return manifest, err
	}
	var tail [32768]byte
	padding := 0
	for {
		if err = ctx.Err(); err != nil {
			return manifest, errors.New("profile verification canceled")
		}
		n, readErr := gz.Read(tail[:])
		padding += n
		if padding > 1<<20 {
			return manifest, errors.New("profile trailing padding exceeds its bound")
		}
		if n > 0 && bytes.Count(tail[:n], []byte{0}) != n {
			return manifest, errors.New("profile archive contains trailing payload")
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return manifest, errors.New("profile gzip checksum is invalid")
		}
	}
	if _, err = buffered.Peek(1); !errors.Is(err, io.EOF) {
		return manifest, errors.New("profile archive contains additional compressed streams")
	}
	after, err := archive.Stat()
	if err != nil || !v80SameInfo(opened, after) {
		return manifest, errors.New("profile artifact changed during verification")
	}
	return manifest, nil
}

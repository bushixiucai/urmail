package main

// Safe v80 FILE restore apply into a brand-new private staging root.
//
// Scope contract (files-restored-only):
//   - The verified fixed manifest (verifyV80Bundle) is the only extraction
//     authority. Archive member paths are re-checked against the fixed profile
//     allowlist; no arbitrary tar path is ever honored.
//   - Destination must be new/empty with operator/root-controlled private
//     ancestry, must never be the real root, a production profile path, or any
//     inode alias of either. All tree operations go through os.Root (no symlink
//     parent traversal); symlinks are extracted LAST, preserved as text, and
//     never followed on the host.
//   - Archive, member content, mode, UID/GID and nanosecond mtime are verified
//     before (verifyV80Bundle), during (re-hash while writing) and after
//     (re-read from disk) extraction. Numeric chown is applied on open file
//     descriptors; only a root operator can satisfy foreign ownership.
//   - Hardlinks, devices, FIFOs, cyclic/escaping/uncovered links are already
//     rejected by the existing verifier and are re-checked here.
//   - Any failure leaves the partial private stage in place, clearly marked,
//     and refuses later reuse. The source artifacts are never modified.
//   - Nothing is executed, started, released, replayed or activated: no PHP or
//     config execution, no services, no holds, no queues, no database, no
//     credential/session activation, no cutover claim.

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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	v80StageMarkerName  = ".urmail-v80-restore-incomplete"
	v80StageReceiptName = "urmail-v80-restore-receipt.json"
)

// v80RestoreReceipt is the explicit files-restored-only completion record. It
// is written inside the private stage and never claims cutover readiness.
type v80RestoreReceipt struct {
	Profile              string    `json:"profile"`
	Scope                string    `json:"scope"`
	Stage                string    `json:"stage"`
	StageMode            uint32    `json:"stage_mode"`
	ManifestSHA256       string    `json:"manifest_sha256"`
	ArchiveSHA256        string    `json:"archive_sha256"`
	LayoutSHA256         string    `json:"layout_sha256"`
	MemberCount          int       `json:"member_count"`
	RegularFileCount     int       `json:"regular_file_count"`
	DirectoryCount       int       `json:"directory_count"`
	SymlinkCount         int       `json:"symlink_count"`
	FilesRestoredOnly    bool      `json:"files_restored_only"`
	ProductionActivation bool      `json:"production_activation"`
	CutoverReady         bool      `json:"cutover_ready"`
	ServicesStarted      bool      `json:"services_started"`
	PHPExecuted          bool      `json:"php_or_config_executed"`
	CredentialsActivated bool      `json:"source_credentials_or_sessions_activated"`
	DatabaseConnected    bool      `json:"database_connected"`
	HoldsReleased        bool      `json:"holds_or_quarantine_released"`
	QueuesReplayed       bool      `json:"postfix_queues_replayed"`
	OwnershipApplied     bool      `json:"numeric_ownership_applied"`
	CompletedAt          time.Time `json:"completed_at"`
	RequiredFollowUp     []string  `json:"required_follow_up"`
}

type v80StagePartial struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// v80ProductionAbsoluteRoots returns the fixed host paths this apply must
// never target: the real root and every profile root rendered absolute.
func v80ProductionAbsoluteRoots() []string {
	roots := []string{"/"}
	for _, spec := range v80Roots {
		roots = append(roots, "/"+spec.Path)
	}
	return roots
}

// v80OverlapsProductionPath rejects a candidate that is equal to, inside of,
// or a parent of any fixed production path (string level, pre-creation).
func v80OverlapsProductionPath(candidate string) bool {
	clean := path.Clean("/" + strings.TrimPrefix(candidate, "/"))
	for _, production := range v80ProductionAbsoluteRoots() {
		if clean == production || strings.HasPrefix(clean, production+"/") || strings.HasPrefix(production, clean+"/") {
			return true
		}
	}
	return false
}

// v80NearestExistingAncestor walks up from absolute to the first existing
// path component, or "" when ancestry cannot be inspected.
func v80NearestExistingAncestor(absolute string) string {
	current := filepath.Clean(absolute)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return current
		}
		if !os.IsNotExist(err) {
			return ""
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}

// v80ValidateStageChain verifies every real directory from absolute up to and
// including anchor: no symlinks, owned by the restoring operator (root when
// run as root) and not writable by unrelated users without the sticky bit.
func v80ValidateStageChain(anchor, absolute string) error {
	owner := os.Geteuid()
	for current := filepath.Clean(absolute); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("staging ancestry must contain only real directories")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != owner && stat.Uid != 0 {
			return errors.New("staging ancestry must be controlled by the restoring operator")
		}
		if info.Mode().Perm()&0o022 != 0 && (current == filepath.Clean(absolute) || info.Mode()&os.ModeSticky == 0) {
			return errors.New("staging ancestry is writable by unrelated users")
		}
		if current == filepath.Dir(current) {
			return nil
		}
		if current == filepath.Dir(current) {
			return errors.New("staging ancestry cannot be validated")
		}
	}
}

// v80RootMkdirAll creates name and its missing parents inside the os.Root.
// Every component is checked with no-follow semantics by the root capability.
func v80RootMkdirAll(root *os.Root, name string) error {
	clean := path.Clean(name)
	if clean == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(clean, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				return errors.New("stage path component is not a directory")
			}
			continue
		}
		if !os.IsNotExist(err) {
			return errors.New("stage path component cannot be inspected")
		}
		if err = root.Mkdir(current, 0o700); err != nil {
			return errors.New("stage directory cannot be created")
		}
	}
	return nil
}

// v80MarkStagePartial best-records an incomplete-stage marker inside a failing
// stage. The stage is never removed; the marker makes accidental reuse
// impossible without an explicit manual review.
func v80MarkStagePartial(stage string, root *os.Root, reason string) {
	record, err := json.Marshal(v80StagePartial{Reason: reason, At: time.Now().UTC()})
	if err != nil {
		return
	}
	_ = v80MarkStagePartialWrite(stage, root, record)
}

func v80MarkStagePartialWrite(stage string, root *os.Root, record []byte) error {
	if root != nil {
		file, err := root.OpenFile(v80StageMarkerName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			_, writeErr := file.Write(record)
			syncErr := file.Sync()
			closeErr := file.Close()
			if writeErr == nil && syncErr == nil && closeErr == nil {
				return nil
			}
		}
	}
	return errors.New("private stage marker could not be persisted through its root capability")
}

// runV80RestoreApply is the root operator entry point for the files-restored-only
// v80 apply. It is intentionally not wired into the CLI dispatch yet; the parent
// review wires it explicitly after rehearsal.
func runV80RestoreApply(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore v80-preserve-apply", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "explicit fixed legacy file profile")
	manifestPath := flags.String("manifest", "", "verified private profile manifest")
	archivePath := flags.String("archive", "", "verified private profile bundle")
	stage := flags.String("stage", "", "brand-new private file staging root")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *profile != v80Profile {
		return errors.New("invalid v80 file restore apply arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("v80 file restore apply requires a root operator")
	}
	if *manifestPath == "" || *archivePath == "" || *stage == "" {
		return errors.New("v80 file restore apply requires --manifest --archive and --stage")
	}
	stageAbsolute, err := filepath.Abs(*stage)
	if err != nil {
		return errors.New("invalid staging destination")
	}
	anchor := v80NearestExistingAncestor(stageAbsolute)
	if anchor == "" || validatePrivateBackupDirectory(anchor) != nil {
		return errors.New("staging ancestry must be root-controlled private directories")
	}
	receipt, applyErr := applyV80RestoreFiles(ctx, *manifestPath, *archivePath, stageAbsolute)
	if applyErr != nil {
		return errors.New("v80 file restore apply failed: staged files only; source artifacts and production state were not changed; inspect the incomplete-stage marker")
	}
	return json.NewEncoder(output).Encode(receipt)
}

// applyV80RestoreFiles extracts a fully verified v80 file bundle into a
// brand-new private staging root and returns the files-restored-only receipt.
// It never activates, executes, starts or releases anything, and never removes
// a failed stage.
func applyV80RestoreFiles(ctx context.Context, manifestPath, archivePath, stage string) (v80RestoreReceipt, error) {
	var receipt v80RestoreReceipt
	stageAbsolute, err := filepath.Abs(stage)
	if err != nil {
		return receipt, errors.New("invalid staging destination")
	}
	if v80OverlapsProductionPath(stageAbsolute) {
		return receipt, errors.New("staging destination collides with the real root, a production profile path or a source root")
	}
	for _, input := range []string{manifestPath, archivePath} {
		if err = v80PrivateStageInput(input); err != nil {
			return receipt, err
		}
	}
	manifestDigest, err := fileSHA256(manifestPath)
	if err != nil {
		return receipt, errors.New("manifest digest cannot be computed")
	}
	manifest, err := verifyV80Bundle(ctx, manifestPath, archivePath)
	if err != nil {
		return receipt, err
	}
	afterManifest, err := fileSHA256(manifestPath)
	if err != nil || afterManifest != manifestDigest {
		return receipt, errors.New("manifest changed during verification")
	}
	anchor := v80NearestExistingAncestor(stageAbsolute)
	if anchor == "" {
		return receipt, errors.New("staging ancestry cannot be inspected")
	}
	if err = v80ValidateStageChain(anchor, anchor); err != nil {
		return receipt, err
	}
	if _, statErr := os.Lstat(stageAbsolute); statErr == nil || !os.IsNotExist(statErr) {
		return receipt, errors.New("staging destination must be brand new; existing stages are never reused")
	}
	parent, err := os.OpenRoot(anchor)
	if err != nil {
		return receipt, errors.New("staging parent capability cannot be opened")
	}
	defer func() { _ = parent.Close() }()
	relative, err := filepath.Rel(anchor, stageAbsolute)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return receipt, errors.New("staging destination escaped its parent capability")
	}
	if err = v80RootMkdirAll(parent, filepath.ToSlash(filepath.Dir(relative))); err != nil {
		return receipt, err
	}
	if err = parent.Mkdir(filepath.ToSlash(relative), 0o700); err != nil {
		return receipt, errors.New("brand-new private staging root cannot be reserved")
	}
	if err = v80ValidateStageChain(anchor, stageAbsolute); err != nil {
		return receipt, err
	}

	root, err := os.OpenRoot(stageAbsolute)
	if err != nil {
		v80MarkStagePartial(stageAbsolute, nil, "stage root capability cannot be opened")
		return receipt, errors.New("staging root capability cannot be opened")
	}
	defer func() { _ = root.Close() }()
	if err = v80MarkStagePartialWrite(stageAbsolute, root, []byte(`{"reason":"FILES_STAGE_IN_PROGRESS"}`)); err != nil {
		return receipt, errors.New("private incomplete marker cannot be reserved")
	}
	failure := func(err error) (v80RestoreReceipt, error) {
		v80MarkStagePartial(stageAbsolute, root, err.Error())
		return v80RestoreReceipt{}, err
	}
	if chmodErr := root.Chmod(".", 0o700); chmodErr != nil {
		return failure(errors.New("staging destination cannot be forced private"))
	}
	stageInfo, statErr := root.Lstat(".")
	if statErr != nil {
		return failure(errors.New("staging destination identity cannot be read"))
	}
	for _, production := range v80ProductionAbsoluteRoots() {
		productionInfo, productionErr := os.Lstat(production)
		if productionErr != nil {
			continue
		}
		if os.SameFile(stageInfo, productionInfo) {
			return failure(errors.New("staging destination is an inode alias of the real root or a production path"))
		}
	}
	// The manifest is verified; members are re-checked defensively so no code
	// path below can be reached by an unexpected path, type or privileged mode.
	members := map[string]v80Member{}
	directories := []string{}
	files := []string{}
	links := []string{}
	for _, member := range manifest.Members {
		if !v80Allowed(member.Name) {
			return failure(errors.New("profile member is outside the fixed allowlist"))
		}
		switch member.Kind {
		case "directory":
			if err = v80RootMkdirAll(root, member.Name); err != nil {
				return failure(err)
			}
			directories = append(directories, member.Name)
		case "file":
			files = append(files, member.Name)
		case "symlink":
			links = append(links, member.Name)
		default:
			return failure(errors.New("profile contains an unsupported object type"))
		}
		members[member.Name] = member
		if err = v80RootMkdirAll(root, path.Dir(member.Name)); err != nil {
			return failure(err)
		}
	}
	archiveDigest, counts, err := v80ExtractStage(ctx, root, archivePath, manifest.Artifact, members, links)
	if err != nil {
		return failure(err)
	}
	if err = v80FinalizeStageDirectories(root, members, directories); err != nil {
		return failure(err)
	}
	if err = v80VerifyRestoredStage(root, members, directories, files, links); err != nil {
		return failure(err)
	}
	directoriesSorted := append([]string(nil), directories...)
	sort.Sort(sort.Reverse(sort.StringSlice(directoriesSorted)))
	for _, name := range directoriesSorted {
		directory, openErr := root.OpenFile(name, os.O_RDONLY, 0)
		if openErr != nil {
			return failure(errors.New("stage directory cannot be synced"))
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return failure(errors.New("stage directory cannot be synced"))
		}
	}
	finalManifest, err := fileSHA256(manifestPath)
	if err != nil || finalManifest != manifestDigest {
		return failure(errors.New("manifest changed during staging"))
	}
	receipt = v80RestoreReceipt{
		Profile:              v80Profile,
		Scope:                "files-restored-only",
		Stage:                stageAbsolute,
		StageMode:            uint32(stageInfo.Mode().Perm()),
		ManifestSHA256:       manifestDigest,
		ArchiveSHA256:        archiveDigest,
		LayoutSHA256:         manifest.LayoutSHA256,
		MemberCount:          len(manifest.Members),
		RegularFileCount:     counts.files,
		DirectoryCount:       counts.directories,
		SymlinkCount:         counts.links,
		FilesRestoredOnly:    true,
		ProductionActivation: false,
		CutoverReady:         false,
		ServicesStarted:      false,
		PHPExecuted:          false,
		CredentialsActivated: false,
		DatabaseConnected:    false,
		HoldsReleased:        false,
		QueuesReplayed:       false,
		OwnershipApplied:     os.Geteuid() == 0,
		CompletedAt:          time.Now().UTC(),
		RequiredFollowUp:     append([]string(nil), v80FollowUp...),
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return failure(errors.New("restore receipt cannot be encoded"))
	}
	receiptFile, err := root.OpenFile(v80StageReceiptName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return failure(errors.New("restore receipt cannot be written"))
	}
	if _, err = receiptFile.Write(raw); err != nil {
		_ = receiptFile.Close()
		return failure(errors.New("restore receipt cannot be written"))
	}
	if err = receiptFile.Sync(); err != nil {
		_ = receiptFile.Close()
		return failure(errors.New("restore receipt cannot be flushed"))
	}
	if err = receiptFile.Close(); err != nil {
		return failure(errors.New("restore receipt cannot be closed"))
	}
	if err = root.Remove(v80StageMarkerName); err != nil {
		return failure(errors.New("stage incomplete marker cannot be cleared"))
	}
	stageDirectory, err := root.Open(".")
	if err != nil {
		return failure(errors.New("staging destination cannot be synced"))
	}
	syncErr := stageDirectory.Sync()
	closeErr := stageDirectory.Close()
	if syncErr != nil || closeErr != nil {
		return failure(errors.New("staging destination cannot be synced"))
	}
	return receipt, nil
}

type v80ExtractCounts struct {
	files       int
	directories int
	links       int
}

// v80ExtractStage streams the verified archive once through the os.Root.
// Directories were pre-created from the manifest, regular files are written
// with content re-hashing and fd-based metadata, and symlinks are recorded to
// be created LAST so no archive path can ever traverse one.
func v80ExtractStage(ctx context.Context, root *os.Root, archivePath string, artifact backupArtifact, members map[string]v80Member, links []string) (string, v80ExtractCounts, error) {
	counts := v80ExtractCounts{}
	archive, err := os.Open(archivePath) // #nosec G304 G703 -- archive identity and hash were verified immediately before this read-only open.
	if err != nil {
		return "", counts, errors.New("verified archive cannot be reopened")
	}
	defer func() { _ = archive.Close() }()
	opened, statErr := archive.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || opened.Size() != artifact.Size {
		return "", counts, errors.New("verified archive changed before extraction")
	}
	streamHash := sha256.New()
	buffered := bufio.NewReader(io.TeeReader(archive, streamHash))
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return "", counts, errors.New("verified archive is not a gzip stream")
	}
	defer func() { _ = gz.Close() }()
	gz.Multistream(false)
	reader := tar.NewReader(v80ContextReader{ctx, gz})
	extracted := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return "", counts, errors.New("extraction canceled")
		}
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", counts, errors.New("verified archive is truncated or invalid")
		}
		member, ok := members[header.Name]
		if !ok || extracted[header.Name] || !v80Allowed(header.Name) {
			return "", counts, errors.New("archive path is not covered by the verified manifest")
		}
		kind := ""
		switch header.Typeflag {
		case tar.TypeReg:
			kind = "file"
		case tar.TypeDir:
			kind = "directory"
		case tar.TypeSymlink:
			kind = "symlink"
		default:
			// Hardlinks, devices, FIFOs and every other special type were
			// already rejected by the existing verifier and are re-rejected here.
			return "", counts, errors.New("archive contains an unsupported special object")
		}
		if kind != member.Kind || header.Mode != member.Mode || header.Uid != member.UID || header.Gid != member.GID || header.Size != member.Size || !header.ModTime.Equal(member.ModifiedAt) || header.Linkname != member.LinkTarget {
			return "", counts, errors.New("archive member metadata no longer matches the verified manifest")
		}
		extracted[header.Name] = true
		switch kind {
		case "directory":
			info, statErr := root.Lstat(header.Name)
			if statErr != nil || !info.IsDir() {
				return "", counts, errors.New("profile directory is missing from the stage")
			}
			counts.directories++
		case "file":
			if err = v80ExtractStageFile(ctx, root, member, reader); err != nil {
				return "", counts, err
			}
			counts.files++
		case "symlink":
			// Recorded only; creation happens after the stream ends.
		}
	}
	if len(extracted) != len(members) {
		return "", counts, errors.New("verified archive is missing manifest members")
	}
	// Symlinks are created only after every regular file exists: preserved as
	// exact text targets, never resolved, never followed on the host.
	for _, name := range links {
		member := members[name]
		if err = root.Symlink(member.LinkTarget, name); err != nil {
			return "", counts, errors.New("profile symlink cannot be preserved in the stage")
		}
		if err = root.Lchown(name, member.UID, member.GID); err != nil {
			return "", counts, errors.New("profile symlink ownership cannot be preserved")
		}
		if err = v80RestoreLinkTime(root, name, member.ModifiedAt); err != nil {
			return "", counts, errors.New("profile symlink nanosecond mtime cannot be preserved")
		}
		counts.links++
	}
	var tail [32768]byte
	padding := 0
	for {
		if ctx.Err() != nil {
			return "", counts, errors.New("extraction canceled")
		}
		n, e := gz.Read(tail[:])
		padding += n
		if padding > 1<<20 || bytes.Count(tail[:n], []byte{0}) != n {
			return "", counts, errors.New("archive trailing payload rejected")
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", counts, errors.New("archive gzip checksum rejected")
		}
	}
	if _, err = buffered.Peek(1); !errors.Is(err, io.EOF) {
		return "", counts, errors.New("archive extra compressed stream rejected")
	}
	after, err := archive.Stat()
	digest := hex.EncodeToString(streamHash.Sum(nil))
	if err != nil || !v80SameInfo(opened, after) || digest != artifact.SHA256 {
		return "", counts, errors.New("actual extracted archive digest or identity changed")
	}
	return digest, counts, nil
}

// v80ExtractStageFile writes one verified regular file inside the os.Root with
// exclusive creation, content re-hashing, an fsync boundary, fd-based numeric
// chown/chmod and a guarded mtime restore (the stage holds no symlinks yet).
func v80ExtractStageFile(ctx context.Context, root *os.Root, member v80Member, reader *tar.Reader) error {
	if err := v80RootMkdirAll(root, path.Dir(member.Name)); err != nil {
		return err
	}
	file, err := root.OpenFile(member.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("stage file cannot be created")
	}
	contentHash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(file, contentHash), v80ContextReader{ctx, reader}, member.Size)
	syncErr := file.Sync()
	chownErr := file.Chown(member.UID, member.GID)
	chmodErr := file.Chmod(v80RestoredMode(member.Mode))
	statErr := file.Close()
	if copyErr != nil || written != member.Size || syncErr != nil || chmodErr != nil || chownErr != nil || statErr != nil {
		return errors.New("stage file content or metadata cannot be applied")
	}
	if hex.EncodeToString(contentHash.Sum(nil)) != member.SHA256 {
		return errors.New("archive member content changed during extraction")
	}
	info, err := root.Lstat(member.Name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != member.Size {
		return errors.New("stage file identity changed during extraction")
	}
	// No symlink exists anywhere in the stage at this point (links are created
	// last), so a guarded path-based time restore cannot traverse a link.
	if err = root.Chtimes(member.Name, member.ModifiedAt, member.ModifiedAt); err != nil {
		return errors.New("stage file modification time cannot be restored")
	}
	return nil
}

// v80FinalizeStageDirectories applies final directory modes, numeric
// ownership, mtimes and fsync, deepest first, after all children exist.
func v80FinalizeStageDirectories(root *os.Root, members map[string]v80Member, directories []string) error {
	ordered := append([]string(nil), directories...)
	sort.Sort(sort.Reverse(sort.StringSlice(ordered)))
	for _, name := range ordered {
		member := members[name]
		directory, err := root.OpenFile(name, os.O_RDONLY, 0)
		if err != nil {
			return errors.New("stage directory cannot be opened for metadata")
		}
		chownErr := directory.Chown(member.UID, member.GID)
		chmodErr := directory.Chmod(v80RestoredMode(member.Mode))
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if chmodErr != nil || chownErr != nil || syncErr != nil || closeErr != nil {
			return errors.New("stage directory metadata cannot be applied")
		}
	}
	for _, name := range ordered {
		member := members[name]
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() {
			return errors.New("stage directory changed before its time restore")
		}
		if err = root.Chtimes(name, member.ModifiedAt, member.ModifiedAt); err != nil {
			return errors.New("stage directory modification time cannot be restored")
		}
	}
	return nil
}

// v80VerifyRestoredStage re-reads the finished stage from disk and requires an
// exact match of the verified manifest: type, mode, size, content hash,
// numeric ownership, nanosecond mtime, symlink text, and no extra entries.
func v80VerifyRestoredStage(root *os.Root, members map[string]v80Member, directories, files, links []string) error {
	ancestors := map[string]bool{}
	for name := range members {
		for current := path.Dir(name); current != "."; current = path.Dir(current) {
			ancestors[current] = true
		}
	}
	expected := map[string]bool{v80StageMarkerName: true}
	for name := range members {
		expected[name] = true
	}
	for name := range ancestors {
		expected[name] = true
	}
	seen := map[string]bool{}
	walkErr := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("stage tree cannot be inspected")
		}
		if name == "." {
			return nil
		}
		if seen[name] || !expected[name] {
			return errors.New("stage contains an entry outside the verified manifest")
		}
		seen[name] = true
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if len(seen) != len(expected) {
		return errors.New("stage tree is missing verified manifest entries")
	}
	for _, name := range files {
		member := members[name]
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("restored file is missing or has the wrong type")
		}
		if info.Size() != member.Size || !v80RestoredModeMatches(info.Mode(), member.Mode) {
			return errors.New("restored file size or mode does not match the verified manifest")
		}
		if !v80RestoredOwnershipMatches(info, member) || !info.ModTime().Equal(member.ModifiedAt) {
			return errors.New("restored file ownership or mtime does not match the verified manifest")
		}
		content, err := root.Open(name)
		if err != nil {
			return errors.New("restored file cannot be re-read")
		}
		hash := sha256.New()
		_, err = io.Copy(hash, content)
		closeErr := content.Close()
		if err != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
			return errors.New("restored file content does not match the verified manifest")
		}
	}
	for _, name := range directories {
		member := members[name]
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || !v80RestoredModeMatches(info.Mode(), member.Mode) {
			return errors.New("restored directory type or mode does not match the verified manifest")
		}
		if !v80RestoredOwnershipMatches(info, member) || !info.ModTime().Equal(member.ModifiedAt) {
			return errors.New("restored directory ownership or mtime does not match the verified manifest")
		}
	}
	for _, name := range links {
		member := members[name]
		info, err := root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return errors.New("restored link is missing or is not a symlink")
		}
		if !v80RestoredOwnershipMatches(info, member) || !info.ModTime().Equal(member.ModifiedAt) || !v80RestoredModeMatches(info.Mode(), member.Mode) {
			return errors.New("restored symlink metadata does not match the verified manifest")
		}
		target, err := root.Readlink(name)
		if err != nil || target != member.LinkTarget || member.SHA256 != v80MemberMarker(member) {
			return errors.New("restored link text does not match the verified manifest")
		}
	}
	return nil
}

// v80RestoredModeMatches compares permission plus setuid/setgid/sticky bits.
func v80RestoredModeMatches(actual fs.FileMode, memberMode int64) bool {
	mask := fs.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	return actual&mask == v80RestoredMode(memberMode)&mask
}

// v80RestoredOwnershipMatches compares exact numeric UID/GID.
func v80RestoredOwnershipMatches(info os.FileInfo, member v80Member) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == member.UID && int(stat.Gid) == member.GID
}

// v80RestoredMode translates POSIX tar bits to Go's distinct high-bit flags.
func v80RestoredMode(mode int64) fs.FileMode {
	result := fs.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		result |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= os.ModeSticky
	}
	return result
}

// v80RestoreLinkTime changes only the link inode via a confined parent fd.
func v80RestoreLinkTime(root *os.Root, name string, stamp time.Time) error {
	parent, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	directory, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	times := []unix.Timespec{{Sec: stamp.Unix(), Nsec: int64(stamp.Nanosecond())}, {Sec: stamp.Unix(), Nsec: int64(stamp.Nanosecond())}}
	return unix.UtimesNanoAt(int(directory.Fd()), path.Base(name), times, unix.AT_SYMLINK_NOFOLLOW) //nolint:gosec // OS descriptors fit the native syscall int.
}

// v80PrivateStageInput refuses links and caller-writable public artifact files.
func v80PrivateStageInput(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("restore inputs must be private regular files")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("restore input owner must match the operator")
	}
	return nil
}

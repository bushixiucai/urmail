package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/telemetry"
)

// Worker indexes only already durable originals with pinned mailbox ownership.
type Worker struct {
	Pool        *db.Pool
	Spool       string
	Archive     string
	legacyAfter int64
	scanAfter   string
	orphanAfter string
	orphanFound bool
}

// Start retries bounded spool batches and explicit retention until shutdown.
func (w *Worker) Start(ctx context.Context, logger *zap.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if config.RestoreQuarantined() {
			return
		}
		if err := w.Once(ctx); err != nil {
			logger.Error("archive reconciliation incomplete", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Once retains every failed spool item for retry or operator investigation.
func (w *Worker) Once(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("archive database unavailable")
	}
	if config.RestoreQuarantined() {
		return errors.New("archive reconciliation quarantined")
	}
	if w.Spool == "" {
		w.Spool = SpoolRoot
	}
	if w.Archive == "" {
		w.Archive = ArchiveRoot
	}
	if err := os.MkdirAll(w.Spool, 0o700); err != nil {
		return err
	}
	root, err := openPrivateRoot(w.Spool)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil && !errors.Is(err, io.EOF) && len(entries) == 0 {
		return err
	}
	var failures []error
	pending := 0
	for _, entry := range entries {
		if entry.IsDir() && validID(entry.Name()) {
			pending++
		}
	}
	pendingCopies.Set(float64(pending))
	processed := 0
	passed := w.scanAfter == ""
	for _, entry := range entries {
		if !passed {
			if entry.Name() == w.scanAfter {
				passed = true
			}
			continue
		}
		if processed >= 100 {
			break
		}
		processed++
		w.scanAfter = entry.Name()
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".pending-") && validID(strings.TrimPrefix(entry.Name(), ".pending-")) {
			info, infoErr := entry.Info()
			if infoErr != nil {
				failures = append(failures, infoErr)
			} else if info.ModTime().Before(time.Now().Add(-24 * time.Hour)) {
				// A pipe never publishes this name. Only abandoned staging
				// directories are eligible; acknowledged copies use bare IDs.
				if cleanupErr := root.RemoveAll(entry.Name()); cleanupErr != nil {
					failures = append(failures, cleanupErr)
				}
			}
			continue
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".indexed-") && validID(strings.TrimPrefix(entry.Name(), ".indexed-")) {
			if err = root.RemoveAll(entry.Name()); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		if err = w.index(ctx, root, entry.Name()); err != nil {
			failures = append(failures, fmt.Errorf("spool %s: %w", entry.Name(), err))
		}
	}
	if processed < 100 {
		w.scanAfter = ""
	}
	q := store.New(w.Pool)
	if _, expiryErr := q.PlatformExpireArchiveApprovals(ctx); expiryErr != nil {
		failures = append(failures, expiryErr)
	}
	if err = w.backfill(ctx); err != nil {
		failures = append(failures, err)
	}
	if _, err = q.PlatformApplyArchiveRetention(ctx); err != nil {
		failures = append(failures, err)
	}
	candidates, err := q.PlatformListArchivePurgeCandidates(ctx)
	if err != nil {
		failures = append(failures, err)
	} else {
		for _, candidate := range candidates {
			if err = w.purge(ctx, candidate.TenantID, candidate.ID); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err = w.observeOrphans(ctx); err != nil {
		failures = append(failures, err)
	}
	conditionErr := q.PlatformSetOperationalCondition(ctx, store.PlatformSetOperationalConditionParams{AlertKey: "archive:reconciliation", Kind: "archive_reconciliation", Firing: len(failures) > 0})
	if conditionErr != nil {
		failures = append(failures, conditionErr)
	} else {
		telemetry.PublishCondition("archive_reconciliation", len(failures) > 0)
	}
	if err = q.PlatformEnqueueObservabilityAlerts(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (w *Worker) index(ctx context.Context, spool *os.Root, id string) error {
	manifest, raw, err := openManifest(spool, id)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	if manifest.Recipient.TenantID <= 0 || manifest.Recipient.MailboxID <= 0 {
		return errors.New("legacy archive recipient is unbound; manual canonical recovery required")
	}
	q := store.New(w.Pool)
	_, err = q.PlatformResolvePinnedArchiveMailbox(ctx, store.PlatformResolvePinnedArchiveMailboxParams{TenantID: manifest.Recipient.TenantID, MailboxID: manifest.Recipient.MailboxID, Address: manifest.Recipient.Address})
	if err != nil {
		return errors.New("pinned archive mailbox no longer matches canonical ownership")
	}
	content := ExtractMIMEReader(raw, manifest.Size, manifest.SHA256)
	if content.MessageID == "" {
		content.MessageID = "archive-" + manifest.SHA256
	}
	if err = os.MkdirAll(w.Archive, 0o700); err != nil {
		return err
	}
	archive, err := openPrivateRoot(w.Archive)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	relative := fmt.Sprintf("%d/%d/%s.eml", manifest.Recipient.TenantID, manifest.Recipient.MailboxID, id)
	if err = archive.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
		return err
	}
	if err = writeRootStream(ctx, archive, relative, raw, manifest.Size, manifest.SHA256); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		readErr := VerifyOriginal(ctx, w.Archive, filepath.Join(w.Archive, relative), manifest.SHA256)
		if readErr != nil {
			return errors.New("canonical archive collision or hash mismatch")
		}
	}
	if err = syncDirectory(filepath.Join(w.Archive, filepath.Dir(relative))); err != nil {
		return err
	}
	path := filepath.Join(w.Archive, relative)
	tx, err := w.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q = store.New(tx)
	// Lock the exact canonical identity through the indexing transaction. The
	// reserved-identity guards prevent reuse once an archive row references it.
	_, err = q.PlatformResolvePinnedArchiveMailbox(ctx, store.PlatformResolvePinnedArchiveMailboxParams{TenantID: manifest.Recipient.TenantID, MailboxID: manifest.Recipient.MailboxID, Address: manifest.Recipient.Address})
	if err != nil {
		return err
	}
	entry, err := q.PlatformIndexArchiveCopy(ctx, store.PlatformIndexArchiveCopyParams{TenantID: manifest.Recipient.TenantID, MailboxID: pgtype.Int8{Int64: manifest.Recipient.MailboxID, Valid: true}, MessageID: content.MessageID, Path: path, Subject: pgtype.Text{String: content.Subject, Valid: content.Subject != ""}, FromAddr: pgtype.Text{String: content.From, Valid: content.From != ""}, ToAddr: pgtype.Text{String: content.To, Valid: content.To != ""}, ReceivedAt: pgtype.Timestamptz{Time: manifest.ReceivedAt, Valid: true}, SizeBytes: manifest.Size, RawSha256: manifest.SHA256})
	if err != nil {
		return err
	}
	if entry.Path != path {
		lifecycle, e := q.PlatformArchiveCopyLifecycle(ctx, store.PlatformArchiveCopyLifecycleParams{TenantID: manifest.Recipient.TenantID, ID: entry.ID})
		if e != nil {
			return e
		}
		if lifecycle.PurgeStartedAt.Valid || lifecycle.PurgedAt.Valid {
			return errors.New("duplicate original is already purging or purged; durable input retained for explicit reconciliation")
		}
		if e = ValidateOwnershipPath(w.Archive, entry.Path, manifest.Recipient.TenantID, manifest.Recipient.MailboxID); e != nil {
			return e
		}
		e = VerifyOriginal(ctx, w.Archive, entry.Path, manifest.SHA256)
		if errors.Is(e, os.ErrNotExist) {
			relative, e2 := archiveRelative(w.Archive, entry.Path)
			if e2 != nil {
				return e2
			}
			if e2 = archive.MkdirAll(filepath.Dir(relative), 0o700); e2 != nil {
				return e2
			}
			if e2 = writeRootStream(ctx, archive, relative, raw, manifest.Size, manifest.SHA256); e2 != nil {
				return e2
			}
			if e2 = syncDirectory(filepath.Join(w.Archive, filepath.Dir(relative))); e2 != nil {
				return e2
			}
			_, e = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: manifest.Recipient.TenantID, Valid: true}, Action: "archive.original_recovered", TargetType: pgtype.Text{String: "archive", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(entry.ID, 10), Valid: true}, Detail: []byte(`{"source":"matching_durable_copy","integrity":"sha256"}`)})
		}
		if e != nil {
			return errors.New("indexed duplicate original integrity failed; incoming original retained for reconciliation")
		}
	}
	attachments, _ := json.Marshal(content.Attachments)
	if err = q.PlatformIndexArchiveContent(ctx, store.PlatformIndexArchiveContentParams{EntryID: entry.ID, TenantID: manifest.Recipient.TenantID, BodyText: content.Body, BodyHtml: content.BodyHTML, Attachments: attachments, ExtractionError: content.Error, OriginalSha256: manifest.SHA256}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if entry.Path != path {
		if err = archive.Remove(relative); err != nil {
			return err
		}
	}
	// Only a committed canonical index permits acknowledging durable input.
	acknowledged := ".indexed-" + id
	if err = spool.Rename(id, acknowledged); err != nil {
		return err
	}
	if err = syncDirectory(w.Spool); err != nil {
		return err
	}
	if err = spool.RemoveAll(acknowledged); err != nil {
		return err
	}
	return syncDirectory(w.Spool)
}

func (w *Worker) purge(ctx context.Context, tenantID, id int64) error {
	for phase := 0; phase < 2; phase++ {
		tx, err := w.Pool.PlatformTx(ctx)
		if err != nil {
			return err
		}
		q := store.New(tx)
		entry, err := q.PlatformLockArchivePurgeCandidate(ctx, store.PlatformLockArchivePurgeCandidateParams{TenantID: tenantID, ID: id})
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		allowed := true
		if entry.RetentionPreview.Valid {
			allowed, err = q.PlatformLockRetentionPurgePolicy(ctx, store.PlatformLockRetentionPurgePolicyParams{TenantID: tenantID, PreviewID: entry.RetentionPreview})
			if errors.Is(err, pgx.ErrNoRows) {
				allowed = false
				err = nil
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		}
		if err = ValidateOwnershipPath(w.Archive, entry.Path, tenantID, 0); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if !allowed {
			// Policy revocation cancels every not-yet-executed physical cleanup.
			// Clear a durable claim only after proving the original still exists.
			readErr := VerifyOriginal(ctx, w.Archive, entry.Path, entry.RawSha256)
			switch {
			case readErr == nil:
				err = q.PlatformCancelArchivePurgeIntent(ctx, store.PlatformCancelArchivePurgeIntentParams{TenantID: tenantID, EntryID: id})
			case errors.Is(readErr, os.ErrNotExist) && entry.PurgeStartedAt.Valid:
				err = q.PlatformFinishArchivePurge(ctx, store.PlatformFinishArchivePurgeParams{TenantID: tenantID, EntryID: id})
			default:
				err = readErr
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			return err
		}
		if phase == 0 {
			err = q.PlatformBeginArchivePurge(ctx, store.PlatformBeginArchivePurgeParams{TenantID: tenantID, EntryID: id})
			if err == nil && !entry.PurgeStartedAt.Valid {
				_, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Action: "archive.physical_purge_started", TargetType: pgtype.Text{String: "archive", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: []byte(`{"source":"archive_worker"}`)})
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				return err
			}
			continue
		}
		// Hold the entry and policy locks through unlink + finalization. A stop
		// committed first prevents deletion; a stop waits for an executing unlink.
		relative, pathErr := archiveRelative(w.Archive, entry.Path)
		if pathErr != nil {
			_ = tx.Rollback(ctx)
			return pathErr
		}
		readErr := VerifyOriginal(ctx, w.Archive, entry.Path, entry.RawSha256)
		switch {
		case readErr == nil:
			root, openErr := openPrivateRoot(w.Archive)
			if openErr != nil {
				_ = tx.Rollback(ctx)
				return openErr
			}
			err = root.Remove(relative)
			_ = root.Close()
		case errors.Is(readErr, os.ErrNotExist) && entry.PurgeStartedAt.Valid:
			err = nil
		default:
			err = readErr
		}
		if err == nil {
			err = syncDirectory(filepath.Join(w.Archive, filepath.Dir(relative)))
		}
		if err == nil {
			err = q.PlatformFinishArchivePurge(ctx, store.PlatformFinishArchivePurgeParams{TenantID: tenantID, EntryID: id})
		}
		if err == nil {
			_, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, Action: "archive.physical_purged", TargetType: pgtype.Text{String: "archive", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(id, 10), Valid: true}, Detail: []byte(`{"source":"archive_worker"}`)})
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
			_ = store.New(w.Pool).PlatformFailArchivePurge(ctx, store.PlatformFailArchivePurgeParams{TenantID: tenantID, EntryID: id, PurgeError: bounded(err.Error(), 1000)})
		}
		return err
	}
	return nil
}

func archiveRelative(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return "", errors.New("archive path outside fixed root")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 {
		return "", errors.New("invalid archive path shape")
	}
	for _, part := range parts[:2] {
		n, e := strconv.ParseInt(part, 10, 64)
		if e != nil || n <= 0 {
			return "", errors.New("invalid archive owner path")
		}
	}
	if filepath.Ext(parts[2]) != ".eml" {
		return "", errors.New("invalid original extension")
	}
	return relative, nil
}

func (w *Worker) backfill(ctx context.Context) error {
	q := store.New(w.Pool)
	entries, err := q.PlatformListArchiveUnindexedOriginals(ctx, w.legacyAfter)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		w.legacyAfter = entry.ID
		if e := ValidateOwnershipPath(w.Archive, entry.Path, entry.TenantID, entry.MailboxID.Int64); e != nil {
			failures = append(failures, e)
			continue
		}
		raw, readErr := openOriginalFile(w.Archive, entry.Path)
		if readErr != nil {
			failures = append(failures, fmt.Errorf("existing archive %d integrity unavailable: %w", entry.ID, readErr))
			continue
		}
		size, digest, readErr := verifyFile(ctx, raw, entry.RawSha256)
		if readErr != nil {
			_ = raw.Close()
			failures = append(failures, readErr)
			continue
		}
		content := ExtractMIMEReader(raw, size, digest)
		_ = raw.Close()
		attachments, _ := json.Marshal(content.Attachments)
		tx, e := w.Pool.PlatformTx(ctx)
		if e != nil {
			return e
		}
		queries := store.New(tx)
		if e = queries.PlatformSetArchiveOriginalHash(ctx, store.PlatformSetArchiveOriginalHashParams{TenantID: entry.TenantID, ID: entry.ID, RawSha256: digest}); e == nil {
			e = queries.PlatformIndexArchiveContent(ctx, store.PlatformIndexArchiveContentParams{TenantID: entry.TenantID, EntryID: entry.ID, BodyText: content.Body, BodyHtml: content.BodyHTML, Attachments: attachments, ExtractionError: content.Error, OriginalSha256: digest})
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	if len(entries) < 100 {
		w.legacyAfter = 0
	}
	return errors.Join(failures...)
}

func (w *Worker) observeOrphans(ctx context.Context) error {
	if _, err := os.Stat(w.Archive); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	q := store.New(w.Pool)
	scanned := 0
	if w.orphanAfter == "" {
		w.orphanFound = false
	}
	privateRoot, err := openPrivateRoot(w.Archive)
	if err != nil {
		return err
	}
	defer func() { _ = privateRoot.Close() }()
	orphans := false
	err = filepath.WalkDir(w.Archive, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".pending-") && validID(strings.TrimPrefix(entry.Name(), ".pending-")) {
			if path <= w.orphanAfter {
				return nil
			}
			scanned++
			if scanned > 100 {
				return filepath.SkipAll
			}
			w.orphanAfter = path
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if info.ModTime().Before(time.Now().Add(-24 * time.Hour)) {
				relative, e := filepath.Rel(w.Archive, path)
				if e != nil {
					return e
				}
				return privateRoot.Remove(relative)
			}
			return nil
		}
		if filepath.Ext(path) != ".eml" {
			return nil
		}
		if path <= w.orphanAfter {
			return nil
		}
		scanned++
		if scanned > 100 {
			return filepath.SkipAll
		}
		w.orphanAfter = path
		indexed, e := q.PlatformArchivePathIndexed(ctx, path)
		if e != nil {
			return e
		}
		if !indexed {
			w.orphanFound = true
			orphans = true
		}
		return nil
	})
	complete := scanned <= 100
	if complete {
		w.orphanAfter = ""
	}
	if err != nil {
		return err
	}
	if !complete && !orphans {
		return nil
	}
	// Findings never authorize deleting an orphan. Keep its bytes for reconciliation.
	err = q.PlatformSetOperationalCondition(ctx, store.PlatformSetOperationalConditionParams{AlertKey: "archive:orphan_original", Kind: "archive_orphan_original", Firing: w.orphanFound})
	if err == nil {
		telemetry.PublishCondition("archive_orphan_original", w.orphanFound)
	}
	return err
}

// IndexSpoolCopy acknowledges only the current pipe's durable input. Unrelated
// failed recovery items must never prevent an already indexed copy's success.
func (w *Worker) IndexSpoolCopy(ctx context.Context, id string) (returnedErr error) {
	defer func() {
		result := "indexed"
		if returnedErr != nil {
			result = "retained"
		}
		copyResults.WithLabelValues(result).Inc()
	}()
	if w.Pool == nil {
		return errors.New("archive database unavailable")
	}
	if config.RestoreQuarantined() {
		return errors.New("archive indexing quarantined")
	}
	if w.Spool == "" {
		w.Spool = SpoolRoot
	}
	if w.Archive == "" {
		w.Archive = ArchiveRoot
	}
	root, err := openPrivateRoot(w.Spool)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return w.index(ctx, root, id)
}

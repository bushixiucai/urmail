package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/bushixiucai/urmail/internal/db"
)

type restoreTOCBuffer struct{ bytes.Buffer }

func (b *restoreTOCBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, errors.New("restore object list exceeds its bound")
	}
	return b.Buffer.Write(p)
}

// restoreDatabaseArchive preserves superuser-owned pg_trgm and replaces all
// application objects in one transaction under the NOSUPERUSER migration role.
// SQL is generated completely into a private file before any database change.
func restoreDatabaseArchive(ctx context.Context, databaseURL, dump, directory string, operationLock *os.File) error {
	var toc restoreTOCBuffer
	list := exec.CommandContext(ctx, "/usr/bin/pg_restore", "--list", "--", dump) // #nosec G204 G702 -- fixed read-only utility; root-approved artifact is a separate argument, never shell text.
	list.Stdout = &toc
	list.Stderr = io.Discard
	list.ExtraFiles = append(list.ExtraFiles, operationLock)
	if err := list.Run(); err != nil {
		return fmt.Errorf("read restore object list: %w", err)
	}
	filtered, err := db.FilterRestoreTOC(toc.String())
	if err != nil {
		return err
	}
	listFile, err := os.CreateTemp(directory, ".database-restore-*.list")
	if err != nil {
		return err
	}
	listPath := listFile.Name()
	defer func() { _ = os.Remove(listPath) }()
	if _, err = listFile.WriteString(filtered); err == nil {
		err = listFile.Sync()
	}
	closeErr := listFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	sql, err := os.CreateTemp(directory, ".database-restore-*.sql")
	if err != nil {
		return err
	}
	sqlPath := sql.Name()
	defer func() { _ = os.Remove(sqlPath) }()
	defer func() { _ = sql.Close() }()
	if _, err = sql.WriteString(db.RestoreSQLPrefix); err != nil {
		return err
	}
	producer := exec.CommandContext(ctx, "/usr/bin/pg_restore", "--no-owner", "--use-list", listPath, "--file=-", "--", dump) // #nosec G204 G702 -- fixed archive decoder; private TOC/artifact paths are separate arguments.
	producer.Stdout = sql
	producer.Stderr = io.Discard
	producer.ExtraFiles = append(producer.ExtraFiles, operationLock)
	if err = producer.Run(); err != nil {
		return fmt.Errorf("prepare complete restore SQL: %w", err)
	}
	if _, err = sql.WriteString("\nCOMMIT;\n"); err == nil {
		err = sql.Sync()
	}
	if err != nil {
		return err
	}
	if err = sql.Close(); err != nil {
		return err
	}
	consumer, err := postgresCommand(ctx, "psql", databaseURL, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--file", sqlPath)
	if err != nil {
		return err
	}
	consumer.Stdout = io.Discard
	consumer.Stderr = io.Discard
	consumer.ExtraFiles = append(consumer.ExtraFiles, operationLock)
	if err = consumer.Run(); err != nil {
		return fmt.Errorf("atomic database restore failed; writers remain quarantined: %w", err)
	}
	return nil
}

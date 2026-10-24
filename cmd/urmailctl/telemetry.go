package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func runTelemetry(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("telemetry maintenance must run as root")
	}
	if len(args) == 0 || args[0] != "acknowledge-gap" {
		return errors.New("telemetry requires acknowledge-gap --stream NAME --generation NUMBER --reason TEXT")
	}
	flags := flag.NewFlagSet("telemetry acknowledge-gap", flag.ContinueOnError)
	stream := flags.String("stream", "", "configured collector stream")
	generation := flags.Int64("generation", 0, "observed generation after source reconciliation")
	reason := flags.String("reason", "", "operator reconciliation reason")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stream == "" || len(*stream) > 255 || *generation <= 0 || len(strings.TrimSpace(*reason)) < 10 || len(*reason) > 2000 {
		return errors.New("gap acknowledgment requires bounded stream, positive generation and reason (10-2000 bytes)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	count, err := q.PlatformAcknowledgeCollectorGap(ctx, store.PlatformAcknowledgeCollectorGapParams{Stream: *stream, Generation: *generation})
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("collector gap changed, is absent, or source is not fresh; inspect current generation and reconcile before acknowledgment")
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "root_operator", "stream": *stream, "generation": *generation, "reason": strings.TrimSpace(*reason)})
	_, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{Action: "telemetry.gap_acknowledged", TargetType: pgtype.Text{String: "collector", Valid: true}, TargetID: pgtype.Text{String: *stream, Valid: true}, Detail: detail})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Println("collector gap acknowledged; no source offsets, delivery events or tenant attribution were changed")
	return nil
}

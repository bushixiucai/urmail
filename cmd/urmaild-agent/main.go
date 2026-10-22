// Command urmaild-agent is the privileged helper of urMail.cn.
//
// It runs as root and is the only component allowed to write system
// configuration. It listens on a unix socket and accepts structured requests;
// there is no path by which a caller can execute an arbitrary command.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "urmaild-agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registry := agentd.NewRegistry()
	registry.Register("file.write", agentd.WriteFile)
	registry.Register("systemctl.restart", agentd.RestartUnit)
	registry.Register("postconf.set", agentd.PostconfSet)
	registry.Register(agentd.OpPostmap, agentd.Postmap)

	server := agentd.NewServer(cfg.AgentSocket, registry, logger)
	if err := server.Listen(ctx); err != nil {
		return err
	}
	logger.Info("agent listening", "socket", cfg.AgentSocket)

	go func() {
		<-ctx.Done()
		server.Wait()
	}()

	return server.Serve()
}

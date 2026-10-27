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

// version is stamped by the release build.
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 {
			switch os.Args[1] {
			case "--version", "-version":
				fmt.Printf("urmaild-agent %s\n", version)
				return
			case "--help", "-h":
				fmt.Println("Usage: urmaild-agent [--version|--help] (configuration is read from the environment)")
				return
			}
		}
		fmt.Fprintln(os.Stderr, "urmaild-agent: unexpected arguments; use --help")
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "urmaild-agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("privileged agent must run as root")
	}
	if os.Getenv("URMAIL_ENV_FILE") == "" {
		_ = os.Setenv("URMAIL_ENV_FILE", "/etc/urmail/agent.env")
	}
	if err := agentd.ValidateRootEnvironment(os.Getenv("URMAIL_ENV_FILE")); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	manager, err := agentd.NewApplyManager(cfg)
	if err != nil {
		return err
	}
	defer manager.Close()
	stopAccessWatcher := manager.StartMailAccessWatcher(ctx, logger)
	defer stopAccessWatcher()
	registry := agentd.NewProductionRegistry(manager)

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

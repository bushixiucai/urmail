package main

import (
	"context"
	"fmt"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
)

// Certificate paths, keys and consumers are exclusively controlled by root agent.
func runCert(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cert requires issue <domain> or renew")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	op := agentd.OpCertificateRenew
	var params any = struct{}{}
	switch args[0] {
	case "issue":
		if len(args) != 2 {
			return fmt.Errorf("cert issue requires a domain")
		}
		op = agentd.OpCertificateIssue
		params = agentd.CertificateParams{Domain: args[1]}
	case "renew":
		if len(args) != 1 {
			return fmt.Errorf("cert renew accepts no arguments")
		}
	default:
		return fmt.Errorf("unknown certificate command")
	}
	response, err := agent.NewClient(cfg.AgentSocket).Do(ctx, op, params)
	if err != nil {
		return err
	}
	fmt.Println(response.Stdout)
	return nil
}

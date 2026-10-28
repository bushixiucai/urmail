package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

func runDNSAuthority(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: urmailctl dns ACTION DOMAIN [--confirm] [--secondary-host HOST --secondary-ip IP --primary-ip IP --tsig-name NAME]")
	}
	action, domain := args[0], args[1]
	allowed := map[string]bool{"status": true, "prepare": true, "enable": true, "disable-check": true, "disable": true, "rotate": true, "retire": true, "rollback": true, "secondary-configure": true, "secondary-prepare": true, "secondary-export": true, "secondary-install": true, "backup-manifest": true}
	if !allowed[action] {
		return fmt.Errorf("unknown DNS action")
	}
	flags := flag.NewFlagSet("dns", flag.ContinueOnError)
	confirm := flags.Bool("confirm", false, "confirm the named DNS operation")
	host := flags.String("secondary-host", "", "secondary hostname")
	secondaryIP := flags.String("secondary-ip", "", "secondary IP")
	primaryIP := flags.String("primary-ip", "", "primary IP")
	tsig := flags.String("tsig-name", "", "TSIG key name, never its secret")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected DNS arguments")
	}
	if action != "status" && action != "backup-manifest" && !*confirm {
		return fmt.Errorf("DNS mutations and protected bundle export require --confirm")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	params := agentd.DNSAuthorityParams{Domain: strings.ToLower(domain)}
	op := agentd.OpDNSAuthorityStatus
	if action != "status" {
		op = agentd.OpDNSAuthorityManage
		params.Action = action
	}
	if action == "secondary-configure" {
		params.Secondary = &nsd.SecondaryConfig{Hostname: *host, Address: *secondaryIP, PrimaryAddress: *primaryIP, TSIGKeyName: *tsig}
	}
	response, err := agent.NewClient(cfg.AgentSocket).Do(ctx, op, params)
	if err != nil {
		return err
	}
	if response == nil || !json.Valid([]byte(response.Stdout)) {
		return fmt.Errorf("invalid DNS agent response")
	}
	fmt.Println(response.Stdout)
	if op == agentd.OpDNSAuthorityManage && action != "secondary-install" && action != "secondary-export" && action != "backup-manifest" {
		// config render derives and applies the same tenant-bound PostgreSQL snapshot.
		if cfg.DatabaseURLPlatform == "" {
			return fmt.Errorf("platform database is required to converge DNS intent")
		}
		pool, openErr := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
		if openErr != nil {
			return openErr
		}
		defer pool.Close()
		snapshot, assembleErr := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: store.New(pool), DB: pool.Pool}, cfg.PublicIPv4, cfg.Nameservers)
		if assembleErr != nil {
			return assembleErr
		}
		_, applyErr := agent.NewClient(cfg.AgentSocket).Do(ctx, agentd.OpConfigApply, agentd.ApplyParams{Postfix: snapshot.PostfixSnap, DNS: snapshot.NSDSnap, DKIM: snapshot.DKIM, SpamPolicies: snapshot.DovecotNode.SpamPolicies})
		return applyErr
	}
	return nil
}

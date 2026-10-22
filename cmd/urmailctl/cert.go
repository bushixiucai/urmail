package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail/validation"
)

const acmeWebroot = "/var/mail/ssl/acme-challenges"

func runCert(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cert requires issue <domain> or renew")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("cert commands must run as root")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "issue":
		if len(args) < 2 {
			return fmt.Errorf("cert issue requires a domain")
		}
		return issueCertificate(ctx, cfg, args[1])
	case "renew":
		return renewCertificates(ctx)
	default:
		return fmt.Errorf("unknown cert command %q", args[0])
	}
}

func issueCertificate(ctx context.Context, cfg *config.Config, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !validation.IsValidHostname(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	if err := os.MkdirAll(acmeWebroot, 0o755); err != nil {
		return fmt.Errorf("create ACME webroot: %w", err)
	}
	email := "hostmaster@" + cfg.PrimaryDomain
	cmd := exec.CommandContext(ctx, "certbot", "certonly",
		"--webroot", "-w", acmeWebroot,
		"-d", domain,
		"--non-interactive", "--agree-tos", "--keep-until-expiring",
		"--email", email)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certbot issue %s: %w", domain, err)
	}
	return installLiveCertificate(domain)
}

func renewCertificates(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "certbot", "renew", "--quiet")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certbot renew: %w", err)
	}
	entries, err := os.ReadDir("/etc/letsencrypt/live")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "README" {
			continue
		}
		if err := installLiveCertificate(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func installLiveCertificate(domain string) error {
	live := filepath.Join("/etc/letsencrypt/live", domain)
	fullchain := filepath.Join(live, "fullchain.pem")
	privkey := filepath.Join(live, "privkey.pem")
	if _, err := os.Stat(fullchain); err != nil {
		return fmt.Errorf("missing %s: %w", fullchain, err)
	}
	if _, err := os.Stat(privkey); err != nil {
		return fmt.Errorf("missing %s: %w", privkey, err)
	}
	if err := os.MkdirAll(certs.DomainCertDir, 0o750); err != nil {
		return err
	}
	certPath := filepath.Join(certs.DomainCertDir, domain+".cert")
	keyPath := filepath.Join(certs.DomainCertDir, domain+".key")
	if err := copyFile(fullchain, certPath, 0o644); err != nil {
		return err
	}
	if err := copyFile(privkey, keyPath, 0o600); err != nil {
		return err
	}
	// nginx is the only consumer of tenant-domain certificates. Reload is
	// best-effort so a failed renewal does not block the rest of the system.
	_ = exec.Command("systemctl", "reload", "nginx").Run()
	fmt.Printf("installed certificate for %s\n", domain)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".urmail-acme-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dst)
}

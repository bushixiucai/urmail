package mail

import (
	"fmt"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/mail/caddy"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/opendkim"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// ConfigContext bundles configuration inputs for all mail-related services.
type ConfigContext struct {
	PostfixNode postfix.NodeConfig
	PostfixSnap postfix.Snapshot

	DovecotNode dovecot.NodeConfig

	NSDNode nsd.NodeConfig
	NSDSnap nsd.Snapshot

	NginxNode nginx.NodeConfig
	NginxSnap nginx.Snapshot

	DKIM opendkim.Snapshot

	// RenderRoot relocates the rendered tree (compose binds it under the data
	// volume). Empty keeps the historical /etc targets.
	RenderRoot string
}

// RenderAll renders configuration files for Postfix, Dovecot, NSD, and Nginx.
// Returns a map of absolute filesystem path -> file contents.
func RenderAll(ctx ConfigContext) (map[string]string, error) {
	result := make(map[string]string)
	renderRoot := ctx.RenderRoot
	configRoot := "/etc"
	if renderRoot != "" {
		configRoot = renderRoot
	}

	// 1. Postfix
	pfNode := ctx.PostfixNode
	if renderRoot != "" {
		// Same shape contract as the Dovecot IncludePrefix injection below:
		// main.cf will be read from <renderRoot>/postfix as the Postfix
		// config_dir, so the file-map references must name the rendered
		// mount-surface paths, not the host /etc/postfix layout.
		pfNode.ConfigDir = filepath.Join(renderRoot, "postfix")
	}
	pfFiles, err := postfix.Generate(pfNode, ctx.PostfixSnap)
	if err != nil {
		return nil, fmt.Errorf("render postfix: %w", err)
	}
	for rel, content := range pfFiles {
		abs := filepath.Join(configRoot, "postfix", rel)
		result[abs] = content
	}

	// 2. Dovecot
	dcNode := ctx.DovecotNode
	dovecotRoot := filepath.Join(configRoot, "dovecot")
	if renderRoot != "" {
		dcNode.IncludePrefix = dovecotRoot
	}
	dcFiles, err := dovecot.Generate(dcNode)
	if err != nil {
		return nil, fmt.Errorf("render dovecot: %w", err)
	}
	for rel, content := range dcFiles {
		// The compose contract pins the main config at <root>/dovecot/dovecot.conf;
		// the conf.d fragment keeps its historical nesting elsewhere.
		abs := filepath.Join(dovecotRoot, rel)
		if renderRoot != "" && rel == dovecot.FileDovecotConf {
			abs = filepath.Join(dovecotRoot, "dovecot.conf")
		}
		result[abs] = content
	}

	// 3–5. Host-only services (NSD, Nginx, OpenDKIM) and compose Caddy.
	// An injected renderRoot selects the compose topology: Caddy replaces Nginx,
	// and NSD/OpenDKIM host trees under /etc are not rendered into the
	// unprivileged container config volume.
	if renderRoot == "" {
		// 3. NSD
		// NSD splits configuration into nsd.conf.d snippets (zones.conf)
		// and zone database definition text files (/etc/nsd/zones/<domain>.txt).
		nsdFiles, err := nsd.Generate(ctx.NSDNode, ctx.NSDSnap)
		if err != nil {
			return nil, fmt.Errorf("render nsd: %w", err)
		}
		for rel, content := range nsdFiles {
			var abs string
			if rel == nsd.FileZonesConf {
				abs = filepath.Join("/etc/nsd/nsd.conf.d", rel)
			} else {
				abs = filepath.Join("/etc/nsd", rel)
			}
			result[abs] = content
		}

		// 4. Nginx
		ngFiles, err := nginx.Generate(ctx.NginxNode, ctx.NginxSnap)
		if err != nil {
			return nil, fmt.Errorf("render nginx: %w", err)
		}
		for rel, content := range ngFiles {
			abs := filepath.Join("/etc/nginx", rel)
			result[abs] = content
		}

		// 5. OpenDKIM: signing keys plus key/signing/trusted-host tables.
		dkimFiles, err := opendkim.Generate(opendkim.NodeConfig{}, ctx.DKIM)
		if err != nil {
			return nil, fmt.Errorf("render opendkim: %w", err)
		}
		for path, content := range dkimFiles {
			result[path] = content
		}
	} else {
		// 4b. Caddy (compose topology): only inside an injected render root, so a
		// host deployment keeps its /etc tree untouched.
		caddyFiles, caddyErr := caddy.Generate(ctx.NginxSnap)
		if caddyErr != nil {
			return nil, fmt.Errorf("render caddy: %w", caddyErr)
		}
		for rel, content := range caddyFiles {
			result[filepath.Join(renderRoot, "caddy", rel)] = content
		}
	}

	return result, nil
}

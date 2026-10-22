package mail

import (
	"fmt"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
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
}

// RenderAll renders configuration files for Postfix, Dovecot, NSD, and Nginx.
// Returns a map of absolute filesystem path -> file contents.
func RenderAll(ctx ConfigContext) (map[string]string, error) {
	result := make(map[string]string)

	// 1. Postfix
	pfFiles, err := postfix.Generate(ctx.PostfixNode, ctx.PostfixSnap)
	if err != nil {
		return nil, fmt.Errorf("render postfix: %w", err)
	}
	for rel, content := range pfFiles {
		abs := filepath.Join("/etc/postfix", rel)
		result[abs] = content
	}

	// 2. Dovecot
	dcFiles, err := dovecot.Generate(ctx.DovecotNode)
	if err != nil {
		return nil, fmt.Errorf("render dovecot: %w", err)
	}
	for rel, content := range dcFiles {
		abs := filepath.Join("/etc/dovecot", rel)
		result[abs] = content
	}

	// 3. NSD
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

	return result, nil
}

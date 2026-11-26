package main

import (
	"fmt"
	"os"

	"github.com/bushixiucai/urmail/internal/mail/caddy"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

func main() {
	snap := nginx.Snapshot{
		Domains: []nginx.DomainConfig{
			{FQDN: "box.urmail.cn", TLSCertFile: "/etc/ssl/certs/box.pem", TLSKeyFile: "/etc/ssl/private/box.key"},
			{FQDN: "mail.urmail.cn", TLSCertFile: "/etc/ssl/certs/mail.pem", TLSKeyFile: "/etc/ssl/private/mail.key"},
		},
		GrafanaHostname: "box.urmail.cn",
	}
	files, err := caddy.Generate(snap)
	if err != nil {
		panic(err)
	}
	_ = os.WriteFile("/tmp/gfvalidate/Caddyfile", []byte(files[caddy.FileName]), 0o644)
	fmt.Println("rendered", len(files[caddy.FileName]), "bytes")
}

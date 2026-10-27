// Package autoconfig serves read-only client autoconfiguration endpoints:
// Mozilla autoconfig (config-v1.1.xml) and Outlook Autodiscover XML.
//
// Everything is generated from an injected Config by pure functions:
// (cfg, email) -> XML. There are no per-domain hardcoded strings and no
// database access; unknown domains fall back to the deployment's primary
// mail host. Endpoints are unauthenticated but IP rate limited with the
// shared middleware.RateLimitWindow pattern.
package autoconfig

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

// DefaultEASPath is the ActiveSync endpoint exposed by internal/eas.
const DefaultEASPath = "/api/v1/mailbox/eas"

// Config drives XML generation. Host maps are keyed by the lowercased
// email domain; a missing entry falls back to PrimaryMailHost.
type Config struct {
	// PrimaryMailHost is the fallback host for unknown domains and the
	// deployment's canonical mail server (e.g. box.urmail.cn).
	PrimaryMailHost string
	// DomainHosts optionally overrides the mail host per email domain.
	DomainHosts map[string]string
	// POP3Enabled controls whether the POP3 incoming server is advertised.
	POP3Enabled bool
	// EASPath is the Outlook Autodiscover EAS URL path. Defaults to
	// DefaultEASPath; a "-" disables the EAS protocol block.
	EASPath string
	// CalDAVPath and CardDAVPath are URL paths on the mail host used as
	// calendar home / address book URLs. "-" disables the element.
	CalDAVPath  string
	CardDAVPath string
}

// WithDefaults normalizes paths: empty paths take the shared defaults and
// "-" explicitly disables the corresponding element.
func (c Config) WithDefaults() Config {
	out := c
	if out.EASPath == "" {
		out.EASPath = DefaultEASPath
	}
	if out.CalDAVPath == "" {
		out.CalDAVPath = "/caldav/"
	}
	if out.CardDAVPath == "" {
		out.CardDAVPath = "/carddav/"
	}
	return out
}

// disablePath reports whether an explicit "-" disables an element.
func disablePath(v string) bool { return v == "-" }

// HostForDomain returns the mail host for a lowercased domain, falling back
// to PrimaryMailHost when the domain is unknown or blank.
func (c Config) HostForDomain(domain string) string {
	if c.DomainHosts != nil {
		if host, ok := c.DomainHosts[strings.ToLower(domain)]; ok && host != "" {
			return host
		}
	}
	return c.PrimaryMailHost
}

// ParseEmailDomain extracts the lowercased domain part of an email address
// and reports whether the address looks like user@domain.
func ParseEmailDomain(email string) (string, bool) {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "", false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if domain == "" || strings.ContainsAny(domain, " \t/?<>&") {
		return "", false
	}
	return domain, true
}

// xmlEscape escapes text for XML text nodes and double-quoted attributes.
func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	).Replace(s)
}

// MozillaAutoconfigXML renders the Thunderbird config-v1.1.xml document.
// The email address is reflected only after XML escaping; the domain is
// matched against cfg.DomainHosts and otherwise falls back to the primary
// mail host so that unknown domains still receive a usable document.
func MozillaAutoconfigXML(cfg Config, email string) string {
	cfg = cfg.WithDefaults()
	domain, _ := ParseEmailDomain(email)
	host := cfg.HostForDomain(domain)
	id := domain
	if id == "" {
		id = host
	}

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<clientConfig version=\"1.1\">\n")
	b.WriteString("  <emailProvider id=\"" + xmlEscape(id) + "\">\n")
	b.WriteString("    <domain>" + xmlEscape(id) + "</domain>\n")
	b.WriteString("    <displayName>urMail</displayName>\n")
	b.WriteString("    <displayShortName>urMail</displayShortName>\n")

	b.WriteString("    <incomingServer type=\"imap\">\n")
	b.WriteString("      <hostname>" + xmlEscape(host) + "</hostname>\n")
	b.WriteString("      <port>993</port>\n")
	b.WriteString("      <socketType>SSL</socketType>\n")
	b.WriteString("      <username>%EMAILADDRESS%</username>\n")
	b.WriteString("      <authentication>password-cleartext</authentication>\n")
	b.WriteString("    </incomingServer>\n")

	if cfg.POP3Enabled {
		b.WriteString("    <incomingServer type=\"pop3\">\n")
		b.WriteString("      <hostname>" + xmlEscape(host) + "</hostname>\n")
		b.WriteString("      <port>995</port>\n")
		b.WriteString("      <socketType>SSL</socketType>\n")
		b.WriteString("      <username>%EMAILADDRESS%</username>\n")
		b.WriteString("      <authentication>password-cleartext</authentication>\n")
		b.WriteString("    </incomingServer>\n")
	}

	b.WriteString("    <outgoingServer type=\"smtp\">\n")
	b.WriteString("      <hostname>" + xmlEscape(host) + "</hostname>\n")
	b.WriteString("      <port>587</port>\n")
	b.WriteString("      <socketType>STARTTLS</socketType>\n")
	b.WriteString("      <username>%EMAILADDRESS%</username>\n")
	b.WriteString("      <authentication>password-cleartext</authentication>\n")
	b.WriteString("    </outgoingServer>\n")
	b.WriteString("    <outgoingServer type=\"smtp\">\n")
	b.WriteString("      <hostname>" + xmlEscape(host) + "</hostname>\n")
	b.WriteString("      <port>465</port>\n")
	b.WriteString("      <socketType>SSL</socketType>\n")
	b.WriteString("      <username>%EMAILADDRESS%</username>\n")
	b.WriteString("      <authentication>password-cleartext</authentication>\n")
	b.WriteString("    </outgoingServer>\n")
	b.WriteString("  </emailProvider>\n")

	if !disablePath(cfg.CalDAVPath) {
		b.WriteString("  <calendarHome>" + xmlEscape("https://"+host+cfg.CalDAVPath) + "</calendarHome>\n")
	}
	if !disablePath(cfg.CardDAVPath) {
		b.WriteString("  <cardDAV>" + xmlEscape("https://"+host+cfg.CardDAVPath) + "</cardDAV>\n")
	}
	b.WriteString("</clientConfig>\n")
	return b.String()
}

// OutlookAutodiscoverXML renders a minimal valid Outlook Autodiscover
// response advertising the EAS endpoint and the account email address.
func OutlookAutodiscoverXML(cfg Config, email string) string {
	cfg = cfg.WithDefaults()
	domain, _ := ParseEmailDomain(email)
	host := cfg.HostForDomain(domain)

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n")
	b.WriteString("<Autodiscover xmlns=\"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006\">\n")
	b.WriteString("  <Response>\n")
	b.WriteString("    <Account>\n")
	b.WriteString("      <AccountType>email</AccountType>\n")
	b.WriteString("      <Action>settings</Action>\n")
	if !disablePath(cfg.EASPath) {
		b.WriteString("      <Protocol>\n")
		b.WriteString("        <Type>Exchange</Type>\n")
		b.WriteString("        <Server>" + xmlEscape("https://"+host+cfg.EASPath) + "</Server>\n")
		b.WriteString("        <LoginName>" + xmlEscape(email) + "</LoginName>\n")
		b.WriteString("        <AuthRequired>on</AuthRequired>\n")
		b.WriteString("      </Protocol>\n")
	}
	b.WriteString("    </Account>\n")
	b.WriteString("  </Response>\n")
	b.WriteString("</Autodiscover>\n")
	return b.String()
}

// Mount registers the autoconfig endpoints on an arbitrary gin router.
// The endpoints are read-only and unauthenticated; both are IP rate limited
// with the shared middleware.RateLimitWindow pattern.
//
// Parent wiring (internal/api/router.go, at root level before any auth
// middleware group):
//
//	autoconfig.Mount(engine, autoconfig.Config{
//		PrimaryMailHost: "box.urmail.cn",
//		POP3Enabled:     true,
//	})
func Mount(engine gin.IRouter, cfg Config) {
	c := cfg.WithDefaults()
	limit := middleware.RateLimitWindow(30, time.Minute, func(ctx *gin.Context) (string, bool) {
		return ctx.ClientIP(), true
	})

	mozilla := func(ctx *gin.Context) {
		email := ctx.Query("emailaddress")
		ctx.Data(http.StatusOK, "text/xml; charset=utf-8",
			[]byte(MozillaAutoconfigXML(c, email)))
	}
	engine.GET("/mail/config-v1.1.xml", limit, mozilla)

	// RFC 7030-ish well-known location: redirect, preserving the query.
	engine.GET("/.well-known/autoconfig/mail/config-v1.1.xml", func(ctx *gin.Context) {
		ctx.Redirect(http.StatusMovedPermanently, "/mail/config-v1.1.xml?"+ctx.Request.URL.RawQuery)
	})

	engine.POST("/autodiscover/autodiscover.xml", limit, func(ctx *gin.Context) {
		// Outlook wraps the request in a SOAP envelope; for a minimal
		// reply we accept a JSON or form field and tolerate a parse miss.
		var req struct {
			EMailAddress string `json:"EmailAddress" xml:"EMailAddress"`
		}
		_ = ctx.ShouldBind(&req)
		email := req.EMailAddress
		if email == "" {
			email = ctx.PostForm("emailaddress")
		}
		ctx.Data(http.StatusOK, "text/xml; charset=utf-8",
			[]byte(OutlookAutodiscoverXML(c, email)))
	})
}

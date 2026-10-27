// Package carddav implements a CardDAV (RFC 6352) server adapter over the
// mailbox contact foundation in internal/collab. PostgreSQL stays the only
// authority; every contact belongs to one mailbox principal and requests for
// another mailbox fail closed. The handler authenticates the same mailbox
// Bearer sessions the native webmail API uses and performs all tenancy
// predicates itself, so it is mounted outside the gin session middleware.
package carddav

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// MaxCardBytes bounds one uploaded vCard, matching the webmail import limit.
const MaxCardBytes = 200000

// SessionLookup resolves still-valid mailbox sessions by token hash. It is
// satisfied by handlers.StoreMailboxNative in production.
type SessionLookup interface {
	GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error)
}

// Handler serves the CardDAV protocol for one authenticated mailbox.
// The zero value is not usable; construct it with Service, Sessions and Prefix.
type Handler struct {
	// Service is the collaboration service owning contact persistence.
	Service appcollab.Service
	// Sessions authenticates mailbox Bearer tokens.
	Sessions SessionLookup
	// Prefix is the URL path prefix this handler is mounted under, for
	// example "/api/v1/carddav". Empty means the whole request path.
	Prefix string
}

// principal is the authenticated mailbox owning the data.
type principal struct {
	TenantID  int64
	MailboxID int64
}

// ServeHTTP routes one request inside the CardDAV URL space:
//
//	<prefix>/                      principal URL
//	<prefix>/<mailboxID>/          addressbook collection
//	<prefix>/<mailboxID>/<id>.vcf  contact resource
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/.well-known/carddav" {
		// RFC 6764 discovery: redirect to the principal URL without auth.
		http.Redirect(w, r, h.Prefix+"/", http.StatusMovedPermanently)
		return
	}
	if h.Service.Q == nil || h.Sessions == nil {
		http.Error(w, "carddav unavailable", http.StatusServiceUnavailable)
		return
	}
	p, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer realm=\"carddav\"")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest, mailboxID, err := h.splitPath(r.URL.Path)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if mailboxID == 0 {
		// Principal URL: serve discovery here, otherwise point the client
		// at its own mailbox collection.
		switch r.Method {
		case http.MethodOptions:
			h.options(w)
		case "PROPFIND":
			h.servePrincipalPropfind(w, r, p)
		default:
			http.Redirect(w, r, h.Prefix+"/"+strconv.FormatInt(p.MailboxID, 10)+"/", http.StatusFound)
		}
		return
	}
	if mailboxID != p.MailboxID {
		// Fail closed: another mailbox in the same tenant stays invisible.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodOptions:
		h.options(w)
	case http.MethodGet, http.MethodHead:
		if rest == "" {
			h.options(w)
			return
		}
		h.serveGet(w, r, p, mailboxID, rest)
	case http.MethodPut:
		if rest == "" {
			methodNotAllowed(w)
			return
		}
		h.servePut(w, r, p, mailboxID, rest)
	case http.MethodDelete:
		if rest == "" {
			methodNotAllowed(w)
			return
		}
		h.serveDelete(w, r, p, mailboxID, rest)
	case "PROPFIND":
		h.servePropfind(w, r, p, mailboxID, rest)
	case "REPORT":
		h.serveReport(w, r, p, mailboxID, rest)
	default:
		// PROPPATCH, MKCOL, MOVE, COPY, POST, PATCH and anything else are
		// not implemented; never fake success.
		methodNotAllowed(w)
	}
}

// authenticate resolves the Bearer token exactly like the mailbox API
// middleware: sha256 hash, session lookup, expiry check.
func (h *Handler) authenticate(r *http.Request) (principal, bool) {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return principal{}, false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return principal{}, false
	}
	session, err := h.Sessions.GetMailboxSession(r.Context(), mailboxprincipal.HashToken(token))
	if err != nil || session.Address == "" || session.ExpiresAt.Before(time.Now()) {
		return principal{}, false
	}
	if session.TenantID <= 0 || session.MailboxID <= 0 {
		return principal{}, false
	}
	return principal{TenantID: session.TenantID, MailboxID: session.MailboxID}, true
}

// splitPath maps a request path to the mailbox id and the remainder inside
// the collection. The remainder is "" for the collection itself.
func (h *Handler) splitPath(path string) (rest string, mailboxID int64, err error) {
	trimmed := strings.Trim(strings.TrimPrefix(path, h.Prefix), "/")
	if trimmed == "" {
		return "", 0, nil
	}
	head, tail, _ := strings.Cut(trimmed, "/")
	mailboxID, err = strconv.ParseInt(head, 10, 64)
	if err != nil || mailboxID <= 0 {
		return "", 0, errors.New("carddav: malformed path")
	}
	return strings.Trim(tail, "/"), mailboxID, nil
}

// options advertises the DAV classes and methods this server implements.
func (h *Handler) options(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, addressbook")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
	http.Error(w, "method not supported", http.StatusMethodNotAllowed)
}

// splitResource resolves "<id>.vcf" to a contact id. It returns 0 when the
// name is not a numeric resource; the caller then treats a PUT as a create.
func splitResource(rest string) (contactID int64, ok bool) {
	name, found := strings.CutSuffix(rest, ".vcf")
	if !found || name == "" || strings.ContainsAny(name, "/ ") {
		return 0, false
	}
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// restoreEpoch uses the database-backed epoch in production. In-memory
// protocol stores have no restore state and retain their initial generation.
func (h *Handler) restoreEpoch(ctx context.Context) (int64, error) {
	if source, ok := h.Service.Q.(interface {
		PlatformRestoreSyncEpoch(context.Context) (int64, error)
	}); ok {
		return source.PlatformRestoreSyncEpoch(ctx)
	}
	return 1, nil
}

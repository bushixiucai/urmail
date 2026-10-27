package carddav

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// preconditions carries the RFC 7232 conditional request headers mapped onto
// the collab optimistic version counter.
type preconditions struct {
	ifMatch     string // expected ETag; empty means no condition
	ifNoneMatch bool   // If-None-Match: * — resource must not exist
}

// parsePreconditions reads the conditional headers relevant to CardDAV.
func parsePreconditions(r *http.Request) (preconditions, bool) {
	var p preconditions
	if header := r.Header.Get("If-Match"); header != "" {
		p.ifMatch = strings.TrimSpace(header)
	}
	if header := r.Header.Get("If-None-Match"); header != "" {
		// Only the "*" star form is meaningful for address data.
		p.ifNoneMatch = strings.TrimSpace(header) == "*"
		if !p.ifNoneMatch {
			return p, false
		}
	}
	return p, true
}

// checkETag compares one If-Match value with the stored ETag. Weak and list
// forms are accepted per RFC 7232 because clients send either.
func etagMatches(header string, version int64) bool {
	current := etag(version)
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == "*" || candidate == current {
			return true
		}
	}
	return false
}

// servePut creates or updates one contact resource. The collab optimistic
// version is the ETag: a stale If-Match becomes 412, never a lost update.
//
// Deviation (documented): new resources created by PUT to a client-invented
// URL receive a server-assigned identity; the 201 response carries Location
// and ETag so clients converge on the canonical href on their next sync.
func (h *Handler) servePut(w http.ResponseWriter, r *http.Request, p principal, _ int64, rest string) {
	pre, ok := parsePreconditions(r)
	if !ok {
		http.Error(w, "unsupported If-None-Match", http.StatusBadRequest)
		return
	}
	if pre.ifMatch != "" && pre.ifNoneMatch {
		http.Error(w, "conflicting preconditions", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxCardBytes))
	if err != nil {
		http.Error(w, "card too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	contact, err := collab.ParseVCard(string(raw))
	if err != nil {
		http.Error(w, "invalid vCard", http.StatusBadRequest)
		return
	}
	contactID, isResource := splitResource(rest)
	var row store.MailboxContacts
	if isResource {
		existing, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, ID: contactID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// A PUT with If-Match to a missing resource must not create it.
			if pre.ifMatch != "" {
				http.Error(w, "precondition failed", http.StatusPreconditionFailed)
				return
			}
			isResource = false
		} else {
			row = existing
		}
	}
	if isResource {
		if pre.ifNoneMatch {
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		expected := row.Version
		if pre.ifMatch != "" {
			if !etagMatches(pre.ifMatch, row.Version) {
				http.Error(w, "precondition failed", http.StatusPreconditionFailed)
				return
			}
		}
		updated, err := h.Service.UpdateContact(r.Context(), appPrincipal(p), row.ID, expected, contact)
		if errors.Is(err, collab.ErrInvalidContact) {
			http.Error(w, "invalid vCard", http.StatusBadRequest)
			return
		}
		if errors.Is(err, appcollab.ErrConflict) {
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("ETag", etag(updated.Version))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Create. The addressbook name matches the webmail handlers so both
	// paths land in the same collection.
	book, err := h.Service.EnsureAddressbook(r.Context(), appPrincipal(p), "Contacts")
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	created, err := h.Service.CreateContact(r.Context(), appPrincipal(p), book.ID, contact)
	if errors.Is(err, collab.ErrInvalidContact) {
		http.Error(w, "invalid vCard", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("ETag", etag(created.Version))
	w.Header().Set("Location", h.contactHref(p.MailboxID, created.ID))
	w.WriteHeader(http.StatusCreated)
}

// serveDelete tombstones one contact. The change log keeps the tombstone so
// later sync-collection runs report it as a 404 change instead of silently
// resurrecting the resource.
func (h *Handler) serveDelete(w http.ResponseWriter, r *http.Request, p principal, _ int64, rest string) {
	pre, ok := parsePreconditions(r)
	if !ok {
		http.Error(w, "unsupported If-None-Match", http.StatusBadRequest)
		return
	}
	id, isResource := splitResource(rest)
	if !isResource {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	row, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	if pre.ifMatch != "" && !etagMatches(pre.ifMatch, row.Version) {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	if _, err = h.Service.Q.PlatformDeleteContact(r.Context(), store.PlatformDeleteContactParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id, Version: row.Version,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Raced with another writer; the version precondition failed.
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/maildnsreport"
	"github.com/bushixiucai/urmail/internal/mailtransport"
	"github.com/bushixiucai/urmail/internal/store"
)

// TransportSecurityHandler publishes tenant-owned policies and accepts bounded public reports.
type TransportSecurityHandler struct {
	Pool, AppPool *db.Pool
	MailHost      string
	Reconcile     ReconcileTrigger
	Observer      mailtransport.Observer
	IntakeSlots   chan struct{}
}

// NewTransportSecurityHandler enables strict public probes and bounded report intake.
func NewTransportSecurityHandler(platform, app *db.Pool, host string, reconcile ReconcileTrigger) *TransportSecurityHandler {
	return &TransportSecurityHandler{Pool: platform, AppPool: app, MailHost: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "."), Reconcile: reconcile, Observer: mailtransport.PublicObserver{}, IntakeSlots: make(chan struct{}, 4)}
}
func (h *TransportSecurityHandler) owned(c *gin.Context) (store.GetDomainByIDRow, bool) {
	var empty store.GetDomainByIDRow
	tenant, ok := marketingTenant(c)
	if !ok {
		return empty, false
	}
	id, ok := marketingID(c, "id")
	if !ok {
		return empty, false
	}
	if h.Pool == nil || h.AppPool == nil {
		authError(c, 503, "TRANSPORT_UNAVAILABLE")
		return empty, false
	}
	row, err := requestStore(c, store.New(h.AppPool)).GetDomainByID(c.Request.Context(), store.GetDomainByIDParams{TenantID: tenant, ID: id})
	if supportError(c, err) {
		return empty, false
	}
	if row.Status != "active" || !row.VerifiedAt.Valid {
		authError(c, 409, "DNS_OWNERSHIP_REQUIRED")
		return empty, false
	}
	return row, true
}
func transportDTO(row store.DomainTransportSecurity, domain, host string) gin.H {
	p := mailtransport.Publication(row)
	body, _ := maildnsreport.RenderWellKnown(p.Policy)
	return gin.H{"enabled": row.Enabled, "version": row.Version, "mode": row.MtastsMode, "policy_id": row.MtastsPolicyID, "mx": row.PolicyMx, "max_age": row.MaxAge, "policy_url": "https://mta-sts." + domain + maildnsreport.WellKnownPath, "policy_body": body, "report_uri": row.TlsRptRua, "dns_records": p.DNSRecords(host), "testing_since": row.TestingSince, "cache_safe_after": row.CacheSafeAfter, "observed_at": row.ObservedAt, "observation_status": row.ObservationStatus, "observation_detail": json.RawMessage(row.ObservationDetail), "dane_enabled": false}
}

// Get reads the authenticated tenant domain publication.
func (h *TransportSecurityHandler) Get(c *gin.Context) {
	domain, ok := h.owned(c)
	if !ok {
		return
	}
	row, err := requestStore(c, store.New(h.AppPool)).GetDomainTransportSecurity(c.Request.Context(), store.GetDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"enabled": false, "version": 0, "mode": "testing", "dane_enabled": false})
		return
	}
	if supportError(c, err) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, transportDTO(row, domain.Fqdn, h.MailHost))
}

// Publish requires explicit confirmation, a matching version and verified enforcement prerequisites.
func (h *TransportSecurityHandler) Publish(c *gin.Context) {
	domain, ok := h.owned(c)
	if !ok {
		return
	}
	var in struct {
		Mode    string `json:"mode"`
		MaxAge  int32  `json:"max_age"`
		Version int64  `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&in) != nil || !in.Confirm || in.Version < 0 || in.MaxAge < 300 || in.MaxAge > 604800 || !slices.Contains([]string{"testing", "enforce", "none"}, in.Mode) || !validation.IsValidHostname(h.MailHost) || !validation.IsValidHostname("mta-sts."+domain.Fqdn) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, store.New(h.AppPool))
	current, err := q.GetDomainTransportSecurity(c.Request.Context(), store.GetDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		supportError(c, err)
		return
	}
	if (exists && current.Version != in.Version) || (!exists && in.Version != 0) {
		authError(c, 409, "TRANSPORT_VERSION_CONFLICT")
		return
	}
	if (!exists || !current.Enabled) && in.Mode != "testing" {
		authError(c, 409, "TRANSPORT_TESTING_REQUIRED")
		return
	}
	now := time.Now().UTC()
	publication := mailtransport.Publication(current)
	if in.Mode == "enforce" && (!exists || current.MtastsMode != "testing" || !current.TestingSince.Valid || now.Before(current.TestingSince.Time.Add(time.Duration(current.MaxAge)*time.Second)) || current.ObservationStatus != "verified" || !current.ObservedAt.Valid || (now.Before(current.ObservedAt.Time) || now.Sub(current.ObservedAt.Time) > time.Hour) || current.ObservationDigest.String != publication.Digest() || !slices.Equal(current.PolicyMx, []string{h.MailHost})) {
		authError(c, 409, "TRANSPORT_OBSERVATION_REQUIRED")
		return
	}
	if exists && current.Enabled && publication.CheckMXChange(h.MailHost, now) != nil {
		authError(c, 409, "TRANSPORT_POLICY_CACHE_DRAIN_REQUIRED")
		return
	}
	cacheSeconds := in.MaxAge
	if exists && current.MaxAge > cacheSeconds {
		cacheSeconds = current.MaxAge
	}
	row, err := q.PublishDomainTransportSecurity(c.Request.Context(), store.PublishDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID, ExpectedVersion: in.Version, PolicyID: strings.ReplaceAll(uuid.NewString(), "-", ""), Mode: in.Mode, Rua: "https://" + h.MailHost + "/api/v1/tls-reports/" + domain.Fqdn, Mx: []string{h.MailHost}, MaxAge: in.MaxAge, CacheSeconds: float64(cacheSeconds)})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "TRANSPORT_VERSION_CONFLICT")
		return
	}
	if supportError(c, err) {
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.JSON(200, transportDTO(row, domain.Fqdn, h.MailHost))
}

// Disable removes publication only after mode=none has drained the previous cache.
func (h *TransportSecurityHandler) Disable(c *gin.Context) {
	domain, ok := h.owned(c)
	if !ok {
		return
	}
	var in struct {
		Version int64 `json:"version"`
		Confirm bool  `json:"confirm"`
	}
	if c.ShouldBindJSON(&in) != nil || !in.Confirm || in.Version < 1 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	n, err := requestStore(c, store.New(h.AppPool)).DisableDomainTransportSecurity(c.Request.Context(), store.DisableDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID, Version: in.Version})
	if supportError(c, err) {
		return
	}
	if n != 1 {
		authError(c, 409, "TRANSPORT_POLICY_CACHE_DRAIN_REQUIRED")
		return
	}
	if h.Reconcile != nil {
		middleware.AfterCommit(c, h.Reconcile.Trigger)
	}
	c.Status(204)
}

// Observe probes public DNS and TLS after releasing the request transaction.
func (h *TransportSecurityHandler) Observe(c *gin.Context) {
	domain, ok := h.owned(c)
	if !ok {
		return
	}
	actor, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	row, err := requestStore(c, store.New(h.AppPool)).GetDomainTransportSecurity(c.Request.Context(), store.GetDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID})
	if supportError(c, err) {
		return
	}
	if !row.Enabled {
		authError(c, 409, "TRANSPORT_TESTING_REQUIRED")
		return
	}
	publication := mailtransport.Publication(row)
	middleware.AfterCommit(c, func() {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		details, probeErr := h.Observer.Observe(ctx, domain.Fqdn, h.MailHost, publication)
		status := "verified"
		if probeErr != nil {
			status = "failed"
		}
		for _, check := range []string{"dns_id", "mx", "https_policy", "smtp_tls"} {
			if !details[check] {
				status = "failed"
			}
		}
		encoded, _ := json.Marshal(details)
		tx, e := h.AppPool.UserTx(ctx, domain.TenantID, actor)
		if e != nil {
			authError(c, 503, "TRANSPORT_UNAVAILABLE")
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		n, e := store.New(tx).ObserveDomainTransportSecurity(ctx, store.ObserveDomainTransportSecurityParams{TenantID: domain.TenantID, DomainID: domain.ID, Version: row.Version, ObservationDigest: pgtype.Text{String: publication.Digest(), Valid: true}, ObservationStatus: status, ObservationDetail: encoded})
		if e != nil || n != 1 {
			authError(c, 409, "TRANSPORT_VERSION_CONFLICT")
			return
		}
		if e = tx.Commit(ctx); e != nil {
			authError(c, 503, "TRANSPORT_UNAVAILABLE")
			return
		}
		c.JSON(200, gin.H{"status": status, "checks": details, "version": row.Version})
	})
}

// Reports lists only redacted summaries belonging to the current tenant domain.
func (h *TransportSecurityHandler) Reports(c *gin.Context) {
	domain, ok := h.owned(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, store.New(h.AppPool)).ListDomainTLSReports(c.Request.Context(), store.ListDomainTLSReportsParams{TenantID: domain.TenantID, DomainID: domain.ID})
	if supportError(c, err) {
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"id": row.ID, "period_start": row.PeriodStart, "period_end": row.PeriodEnd, "successful": row.Successful, "failed": row.Failed, "failures": json.RawMessage(row.Failures), "received_at": row.ReceivedAt, "source_verified": false})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"reports": out, "limit": 100})
}

// Policy serves public text only for the exact registered mta-sts hostname.
func (h *TransportSecurityHandler) Policy(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	host := c.Request.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasPrefix(host, "mta-sts.") || !validation.IsValidHostname(host) || h.Pool == nil {
		c.Status(404)
		return
	}
	domain := strings.TrimPrefix(host, "mta-sts.")
	row, err := store.New(h.Pool).PlatformPublicTransportSecurity(c.Request.Context(), domain)
	if err != nil {
		c.Status(404)
		return
	}
	policy := maildnsreport.MTASTSPolicy{ID: row.MtastsPolicyID, Mode: maildnsreport.MTASTSMode(row.MtastsMode), MX: row.PolicyMx, MaxAgeSeconds: int(row.MaxAge)}
	body, err := maildnsreport.RenderWellKnown(policy)
	if err != nil {
		c.Status(503)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("ETag", `"`+row.MtastsPolicyID+`"`)
	c.Data(200, "text/plain; charset=utf-8", []byte(body))
}

// Intake retains bounded, deduplicated and explicitly unverified report summaries.
func (h *TransportSecurityHandler) Intake(c *gin.Context) {
	if h.Pool == nil {
		c.Status(503)
		return
	}
	select {
	case h.IntakeSlots <- struct{}{}:
		defer func() { <-h.IntakeSlots }()
	default:
		c.Status(429)
		return
	}
	domain, err := validation.NormalizeDomain(c.Param("fqdn"))
	if err != nil {
		c.Status(400)
		return
	}
	row, err := store.New(h.Pool).PlatformPublicTransportSecurity(c.Request.Context(), domain)
	if err != nil {
		c.Status(404)
		return
	}
	media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !slices.Contains([]string{"application/tlsrpt+json", "application/tlsrpt+gzip", "application/json"}, media) {
		c.Status(415)
		return
	}
	encoding := strings.ToLower(c.GetHeader("Content-Encoding"))
	if encoding != "" && encoding != "gzip" {
		c.Status(415)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maildnsreport.MaxTLSReportWireBytes+1))
	if err != nil || len(raw) > maildnsreport.MaxTLSReportWireBytes {
		c.Status(413)
		return
	}
	report, err := maildnsreport.ParseTLSReport(raw, media == "application/tlsrpt+gzip" || encoding == "gzip", domain, time.Now().UTC())
	if err != nil {
		c.Status(422)
		return
	}
	tx, err := h.Pool.PlatformTx(c.Request.Context())
	if err != nil {
		c.Status(503)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Serialize only this domain's intake and cap daily retained summaries.
	if _, err = tx.Exec(c.Request.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('urmail/tlsrpt/'||$1::bigint::text,0))", row.DomainID); err != nil {
		c.Status(503)
		return
	}
	q := store.New(tx)
	prior, findErr := q.PlatformFindTLSReportFingerprint(c.Request.Context(), store.PlatformFindTLSReportFingerprintParams{TenantID: row.TenantID, DomainID: row.DomainID, ReporterDigest: report.ReporterDigest, ReportIDDigest: report.IDDigest, PeriodStart: pgtype.Timestamptz{Time: report.Start, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: report.End, Valid: true}})
	if findErr == nil {
		if prior != report.Digest {
			c.Status(http.StatusConflict)
		} else {
			c.Status(http.StatusAccepted)
		}
		return
	}
	if !errors.Is(findErr, pgx.ErrNoRows) {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	count, err := q.PlatformCountRecentTLSReports(c.Request.Context(), store.PlatformCountRecentTLSReportsParams{TenantID: row.TenantID, DomainID: row.DomainID})
	if err != nil {
		c.Status(503)
		return
	}
	if count >= 2000 {
		c.Status(429)
		return
	}
	failures, _ := json.Marshal(report.Failures)
	_, err = q.PlatformRecordTLSReport(c.Request.Context(), store.PlatformRecordTLSReportParams{TenantID: row.TenantID, DomainID: row.DomainID, ReportDigest: report.Digest, ReporterDigest: report.ReporterDigest, ReportIDDigest: report.IDDigest, PeriodStart: pgtype.Timestamptz{Time: report.Start, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: report.End, Valid: true}, Successful: report.Successful, Failed: report.Failed, Failures: failures})
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		c.Status(503)
		return
	}
	c.Status(http.StatusAccepted)
}

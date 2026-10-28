package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	report "github.com/bushixiucai/urmail/internal/domain/marketingreport"
	"github.com/bushixiucai/urmail/internal/store"
)

type marketingReportHandler struct{ store *store.Queries }

// RegisterMarketingReportRoutes exposes only the current tenant's bounded
// facts. Export is explicit, audited, and bound to the displayed snapshot.
func RegisterMarketingReportRoutes(group *gin.RouterGroup, q *store.Queries) {
	h := &marketingReportHandler{store: q}
	r := group.Group("/marketing/reports", middleware.RequireRole("owner", "admin"))
	for _, kind := range []string{"campaigns", "flows"} {
		r.GET("/"+kind+"/:id", h.view(kind))
		r.POST("/"+kind+"/:id/export", middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), h.export(kind))
	}
}

type marketingReportScope struct {
	From           string `json:"from"`
	Until          string `json:"until"`
	ExpectedDigest string `json:"expected_digest"`
	Confirmed      bool   `json:"confirmed"`
}

func marketingReportWindow(input marketingReportScope, now time.Time) (time.Time, time.Time, bool) {
	until := now.UTC().Truncate(time.Second)
	from := until.Add(-30 * 24 * time.Hour)
	var err error
	if input.From != "" {
		from, err = time.Parse(time.RFC3339Nano, input.From)
		if err != nil {
			return from, until, false
		}
	}
	if input.Until != "" {
		until, err = time.Parse(time.RFC3339Nano, input.Until)
		if err != nil {
			return from, until, false
		}
	}
	return from.UTC(), until.UTC(), from.Before(until) && until.Sub(from) <= 90*24*time.Hour && from.Year() >= 2000 && until.Before(now.Add(24*time.Hour))
}
func (h *marketingReportHandler) load(c *gin.Context, kind string, input marketingReportScope) (*store.Queries, report.Snapshot, bool) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return nil, report.Snapshot{}, false
	}
	id, ok := marketingID(c, "id")
	if !ok {
		return nil, report.Snapshot{}, false
	}
	from, until, ok := marketingReportWindow(input, time.Now())
	if !ok {
		authError(c, 400, "MARKETING_REPORT_WINDOW")
		return nil, report.Snapshot{}, false
	}
	q := requestStore(c, h.store)
	if q == nil {
		authError(c, 503, "MARKETING_UNAVAILABLE")
		return nil, report.Snapshot{}, false
	}
	if err := q.SetMarketingReportBudget(c); err != nil {
		authError(c, 503, "MARKETING_UNAVAILABLE")
		return nil, report.Snapshot{}, false
	}
	ts := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
	var raw []byte
	var err error
	if kind == "campaigns" {
		raw, err = q.MarketingCampaignReportSnapshot(c, store.MarketingCampaignReportSnapshotParams{TenantID: tenant, ResourceID: id, FromTime: ts(from), UntilTime: ts(until)})
	} else {
		raw, err = q.MarketingFlowReportSnapshot(c, store.MarketingFlowReportSnapshotParams{TenantID: tenant, ResourceID: id, FromTime: ts(from), UntilTime: ts(until)})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return nil, report.Snapshot{}, false
	}
	if err != nil {
		authError(c, 503, "MARKETING_REPORT_UNAVAILABLE")
		return nil, report.Snapshot{}, false
	}
	var payload struct {
		Name string        `json:"name"`
		Rows []report.Fact `json:"rows"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		authError(c, 503, "MARKETING_REPORT_UNAVAILABLE")
		return nil, report.Snapshot{}, false
	}
	snapshot, err := report.Build(report.Scope{Kind: kind, ResourceID: id, TenantID: tenant, From: from, Until: until}, payload.Name, payload.Rows)
	if err != nil {
		authError(c, 413, "MARKETING_REPORT_BUDGET")
		return nil, report.Snapshot{}, false
	}
	if input.ExpectedDigest != "" && input.ExpectedDigest != snapshot.Digest {
		authError(c, 409, "MARKETING_REPORT_CHANGED")
		return nil, report.Snapshot{}, false
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	return q, snapshot, true
}
func (h *marketingReportHandler) view(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		offset, limit := 0, 50
		var err error
		if value := c.Query("offset"); value != "" {
			offset, err = strconv.Atoi(value)
			if err != nil || offset < 0 || offset > report.MaxFacts {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
		}
		if value := c.Query("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 100 {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
		}
		input := marketingReportScope{From: c.Query("from"), Until: c.Query("until"), ExpectedDigest: c.Query("expected_digest")}
		if offset > 0 && (input.ExpectedDigest == "" || input.From == "" || input.Until == "") {
			authError(c, 400, "MARKETING_REPORT_CHANGED")
			return
		}
		_, snapshot, ok := h.load(c, kind, input)
		if !ok {
			return
		}
		if offset > len(snapshot.Rows) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		end := min(offset+limit, len(snapshot.Rows))
		next := 0
		if end < len(snapshot.Rows) {
			next = end
		}
		c.JSON(200, gin.H{"report": gin.H{"schema_version": 1, "kind": kind, "resource_id": snapshot.Scope.ResourceID, "name": snapshot.Name, "from": snapshot.Scope.From, "until": snapshot.Scope.Until, "observed_at": time.Now().UTC(), "digest": snapshot.Digest, "counts": snapshot.Counts, "total_facts": len(snapshot.Rows), "rows": snapshot.Rows[offset:end], "next_offset": next, "max_facts": report.MaxFacts, "max_days": 90, "semantics": "current_outcomes_for_created_cohort;sent_and_submitted_are_SMTP_acceptance_only;unknown_is_not_delivered"}})
	}
}
func (h *marketingReportHandler) export(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input marketingReportScope
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !input.Confirmed || len(input.ExpectedDigest) != 64 || input.From == "" || input.Until == "" {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		q, snapshot, ok := h.load(c, kind, input)
		if !ok {
			return
		}
		observed := time.Now().UTC()
		data, err := report.CSV(snapshot, observed)
		if err != nil {
			authError(c, 413, "MARKETING_REPORT_BUDGET")
			return
		}
		actor, ok := middleware.GetUserID(c)
		if !ok {
			authError(c, 401, "UNAUTHORIZED")
			return
		}
		target := fmt.Sprintf("/marketing/reports/%s/%d?digest=%s&from=%s&until=%s&facts=%d&bytes=%d", kind, snapshot.Scope.ResourceID, snapshot.Digest, snapshot.Scope.From.Format(time.RFC3339Nano), snapshot.Scope.Until.Format(time.RFC3339Nano), len(snapshot.Rows), len(data))
		if err = q.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: snapshot.Scope.TenantID, UserID: actor, Action: "EXPORT marketing.report", Target: target, Status: 200}); err != nil {
			authError(c, 503, "AUDIT_UNAVAILABLE")
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="marketing-%s-%d-%s.csv"`, kind, snapshot.Scope.ResourceID, snapshot.Digest[:12]))
		c.Header("X-UrMail-Report-Digest", snapshot.Digest)
		c.Data(http.StatusOK, "text/csv; charset=utf-8", data)
	}
}

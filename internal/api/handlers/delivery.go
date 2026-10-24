package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// QueueReader is the typed root-agent read-only queue capability.
type QueueReader interface {
	QueueSnapshot(context.Context) (agentd.QueueSnapshot, error)
}

// DeliveryStore reads only the authenticated tenant's delivery outcomes.
type DeliveryStore interface {
	TenantDeliveryOutcomeCounts(context.Context, store.TenantDeliveryOutcomeCountsParams) ([]store.TenantDeliveryOutcomeCountsRow, error)
}

// DeliveryHandler exposes delivery freshness and scoped queue observations.
type DeliveryHandler struct {
	Store    DeliveryStore
	Platform *store.Queries
	Queue    QueueReader
	Stream   string
}

// Health reports terminal-recipient outcomes, with explicit denominator and
// freshness. SMTP/LMTP acceptance never means inbox placement or a read.
func (h *DeliveryHandler) Health(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	window := 24 * time.Hour
	switch c.Query("window") {
	case "", "24h":
	case "1h":
		window = time.Hour
	case "7d":
		window = 7 * 24 * time.Hour
	case "30d":
		window = 30 * 24 * time.Hour
	default:
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	now := time.Now().UTC()
	start := now.Add(-window)
	counts, err := requestStore(c, h.Store).TenantDeliveryOutcomeCounts(c, store.TenantDeliveryOutcomeCountsParams{TenantID: pgtype.Int8{Int64: tenantID, Valid: true}, OccurredAt: pgtype.Timestamptz{Time: start, Valid: true}, WindowEnd: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "TELEMETRY_UNAVAILABLE")
		return
	}
	byStatus := map[string]int64{}
	var terminal int64
	for _, row := range counts {
		byStatus[row.Status] = row.Count
		if row.Status != "deferred" {
			terminal += row.Count
		}
	}
	var rate *float64
	if terminal > 0 {
		value := float64(byStatus["sent"]) / float64(terminal)
		rate = &value
	}
	fresh, observed, gap, healthErr := h.freshness(c, now)
	if healthErr != nil {
		authError(c, http.StatusServiceUnavailable, "TELEMETRY_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"window_start": start, "window_end": now, "recipient_outcomes": byStatus, "terminal_recipients": terminal, "accepted_recipients": byStatus["sent"], "acceptance_rate": rate, "acceptance_semantics": "next_smtp_or_lmtp_accepted", "attribution_semantics": "authenticated_outgoing_or_local_recipient", "fresh": fresh, "observed_at": observed, "collection_gap": gap})
}

func (h *DeliveryHandler) freshness(ctx context.Context, now time.Time) (bool, *time.Time, bool, error) {
	if h.Platform == nil {
		return false, nil, false, nil
	}
	rows, err := h.Platform.PlatformCollectorHealth(ctx)
	if err != nil {
		return false, nil, false, err
	}
	for _, row := range rows {
		if row.Stream == h.Stream && row.ObservedAt.Valid {
			observed := row.ObservedAt.Time
			gap := row.GapReason.Valid
			return now.Sub(observed) >= -time.Minute && now.Sub(observed) <= 2*time.Minute && !gap, &observed, gap, nil
		}
	}
	return false, nil, false, nil
}

// QueueForTenant redacts unrelated recipients and never attributes a queued
// sender from its forgeable envelope address. Authenticated ownership must
// match the correlated queue lifecycle's actual arrival time.
func (h *DeliveryHandler) QueueForTenant(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	if h.Platform == nil || h.Queue == nil {
		authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
		return
	}
	snapshot, err := h.Queue.QueueSnapshot(c)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
		return
	}
	visible := []agentd.QueueEntry{}
	for _, entry := range snapshot.Entries {
		owner, ownerErr := h.Platform.PlatformQueueAuthenticatedTenant(c, store.PlatformQueueAuthenticatedTenantParams{Stream: h.Stream, Instance: h.Stream + "/postfix", QueueID: entry.QueueID, Column4: pgtype.Timestamptz{Time: time.Unix(entry.ArrivalTime, 0), Valid: true}})
		if ownerErr != nil && !errors.Is(ownerErr, pgx.ErrNoRows) {
			authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
			return
		}
		if ownerErr == nil && owner == tenantID {
			visible = append(visible, entry)
			continue
		}
		local := []agentd.QueueRecipient{}
		for _, recipient := range entry.Recipients {
			recipientTenant, recipientErr := h.Platform.PlatformResolveDeliveryMailboxTenant(c, strings.ToLower(recipient.Address))
			if recipientErr != nil && !errors.Is(recipientErr, pgx.ErrNoRows) {
				authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
				return
			}
			if recipientErr == nil && recipientTenant == tenantID {
				local = append(local, recipient)
			}
		}
		if len(local) > 0 {
			entry.Recipients = local
			visible = append(visible, entry)
		}
	}
	snapshot.Entries = visible
	c.JSON(http.StatusOK, snapshot)
}

// PlatformQueue returns unknown as well as attributed envelope metadata. The
// router must protect this endpoint with the platform administrator role.
func (h *DeliveryHandler) PlatformQueue(c *gin.Context) {
	if h.Queue == nil {
		authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
		return
	}
	snapshot, err := h.Queue.QueueSnapshot(c)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// CollectorHealth is an operator-only checkpoint and gap diagnostic endpoint.
func (h *DeliveryHandler) CollectorHealth(c *gin.Context) {
	if h.Platform == nil {
		authError(c, http.StatusServiceUnavailable, "TELEMETRY_UNAVAILABLE")
		return
	}
	rows, err := h.Platform.PlatformCollectorHealth(c)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "TELEMETRY_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"collectors": rows})
}

package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

// FlowContent resolves the immutable text a flow step sends. The wiring
// supplies it, typically from the tenant template named by the step key.
type FlowContent func(ctx context.Context, tenantID, flowID int64, step string) (subject string, body string, ok bool, err error)

// FlowSender connects the marketingflow send step to the tenant's configured
// dedicated gateway. It re-verifies entitlement, unsubscribe and suppression
// in one statement immediately before SMTP, and once more before DATA, so an
// unsubscribe recorded mid-run stops the current and every later step.
type FlowSender struct {
	Pool           *db.Pool
	Secrets        SecretBox
	Tokens         *domainMail.SuppressionManager
	Hostname       string
	UnsubscribeURL string
	Content        FlowContent
}

// Send implements marketingflow.Sender.
func (s *FlowSender) Send(ctx context.Context, tenantID, flowID int64, email, step string) error {
	if s == nil || s.Pool == nil || s.Secrets == nil || s.Tokens == nil || s.Content == nil {
		return ErrChannelUnavailable
	}
	subject, body, ok, err := s.Content(ctx, tenantID, flowID, step)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("marketing flow step %q has no content", step)
	}
	q := store.New(s.Pool.Pool)
	channel, err := q.PlatformGetActiveMarketingChannel(ctx, tenantID)
	if err != nil {
		return ErrChannelUnavailable
	}
	if !strings.HasPrefix(channel.EncryptedConfig, "enc:v1:") {
		return errors.New("marketing channel encryption is required")
	}
	plain, err := s.Secrets.DecryptString(channel.EncryptedConfig)
	if err != nil {
		return errors.New("marketing channel cannot be decrypted")
	}
	var cfg SMTPConfig
	if json.Unmarshal([]byte(plain), &cfg) != nil || cfg.Validate(channel.Kind) != nil {
		return ErrChannelUnavailable
	}
	versioned, err := q.PlatformMarketingFlowIsVersioned(ctx, store.PlatformMarketingFlowIsVersionedParams{TenantID: tenantID, RuntimeFlowID: flowID})
	if err != nil {
		return err
	}
	lease := leaseFromContext(ctx)
	if versioned && lease == nil {
		return marketingflow.ErrNotAllowed
	}
	authorize := func() (bool, error) {
		if lease != nil {
			owned, checkErr := q.PlatformCheckMarketingFlowLease(ctx, store.PlatformCheckMarketingFlowLeaseParams{TenantID: tenantID, FlowID: flowID, Email: email, LeaseOwner: pgtype.Text{String: lease.owner, Valid: true}, LeaseFence: lease.fence})
			if checkErr != nil || !owned {
				return false, checkErr
			}
		}
		if versioned {
			eligible, checkErr := q.PlatformVersionedMarketingFlowMayAdvance(ctx, store.PlatformVersionedMarketingFlowMayAdvanceParams{TenantID: tenantID, RuntimeFlowID: flowID, Email: email})
			if checkErr != nil || !eligible {
				return false, checkErr
			}
		}
		return q.PlatformAuthorizeMarketingFlowSend(ctx, store.PlatformAuthorizeMarketingFlowSendParams{TenantID: tenantID, FlowID: flowID, Email: email, ChannelID: channel.ID})
	}
	allowed, err := authorize()
	if err != nil {
		return err
	}
	if !allowed {
		return s.revoked(ctx, tenantID, flowID, email)
	}
	messageID := fmt.Sprintf("flow-%d-%s@%s", flowID, uuid.NewString(), s.Hostname)
	delivery := Delivery{TenantID: tenantID, CampaignID: flowID, Email: email, Subject: subject, Body: body, MessageID: messageID, EnvelopeFrom: cfg.BounceAddress, UnsubscribeURL: s.unsubscribeURL(tenantID, email)}
	begun := false
	delivery.BeforeData = func(context.Context) error {
		stillAllowed, checkErr := authorize()
		if checkErr != nil {
			return errors.New("marketing flow recheck unavailable")
		}
		if !stillAllowed {
			return s.revoked(ctx, tenantID, flowID, email)
		}
		if versioned {
			result, beginErr := q.PlatformBeginMarketingFlowSend(ctx, store.PlatformBeginMarketingFlowSendParams{PTenant: tenantID, PFlow: flowID, PEmail: email, POwner: lease.owner, PFence: lease.fence, PChannel: channel.ID})
			if beginErr != nil {
				return beginErr
			}
			if result != "authorized" {
				return &flowGateError{code: result}
			}
			begun = true
		}
		return nil
	}
	sendErr := SendSMTP(ctx, cfg, delivery)
	if sendErr != nil && domainMail.IsDeliveryUncertain(sendErr) {
		return fmt.Errorf("%w: %w", marketingflow.ErrUncertain, sendErr)
	}
	if versioned && begun && sendErr != nil {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		released, releaseErr := q.PlatformReleaseMarketingFlowReservation(finishCtx, store.PlatformReleaseMarketingFlowReservationParams{PTenant: tenantID, PFlow: flowID, PEmail: email, POwner: lease.owner, PFence: lease.fence})
		if releaseErr != nil || !released {
			return marketingflow.ErrUncertain
		}
	}
	if sendErr == nil && lease != nil {
		lease.confirmed = true
	}
	return sendErr
}

// revoked marks the whole flow position unsubscribed when the revocation is a
// suppression and tells the engine to stop without sending; suppression is
// terminal for all subsequent steps.
func (s *FlowSender) revoked(ctx context.Context, tenantID, flowID int64, email string) error {
	suppressed, err := store.New(s.Pool.Pool).PlatformCheckMarketingEmailSuppressed(ctx, store.PlatformCheckMarketingEmailSuppressedParams{TenantID: tenantID, Email: email})
	if err != nil {
		return err
	}
	if !suppressed {
		// Entitlement or flow state closed the gate; this is not terminal for
		// the position, so the driver may retry after the grace window.
		return marketingflow.ErrNotAllowed
	}
	if _, err := store.New(s.Pool.Pool).PlatformMarkMarketingFlowUnsubscribed(ctx, store.PlatformMarkMarketingFlowUnsubscribedParams{TenantID: tenantID, FlowID: flowID, Email: email}); err != nil {
		return err
	}
	return nil
}

func (s *FlowSender) unsubscribeURL(tenantID int64, email string) string {
	sep := "?"
	if strings.ContainsRune(s.UnsubscribeURL, '?') {
		sep = "&"
	}
	return s.UnsubscribeURL + sep + "token=" + s.Tokens.GenerateUnsubscribeToken(tenantID, email)
}

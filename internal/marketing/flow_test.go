package marketing

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

func flowFixture(t *testing.T, f campaignFixture, email string) int64 {
	t.Helper()
	ctx := context.Background()
	q := store.New(f.platform.Pool)
	flowID, err := q.PlatformInsertMarketingFlow(ctx, store.PlatformInsertMarketingFlowParams{TenantID: f.tenant, Name: "Welcome"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformInsertMarketingFlowStep(ctx, store.PlatformInsertMarketingFlowStepParams{TenantID: f.tenant, FlowID: flowID, StepKey: "welcome", Kind: "send", NextKey: "later", Position: 0}); err != nil {
		t.Fatal(err)
	}
	position, err := q.PlatformAdvanceMarketingFlow(ctx, store.PlatformAdvanceMarketingFlowParams{TenantID: f.tenant, FlowID: flowID, Email: email, StepKey: "welcome"})
	if err != nil || position.StepKey != "welcome" {
		t.Fatalf("position: %+v %v", position, err)
	}
	return flowID
}

func flowSender(t *testing.T, f campaignFixture) *FlowSender {
	t.Helper()
	tokens, err := domainMail.NewSuppressionManager([]byte(strings.Repeat("u", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &FlowSender{Pool: f.platform, Secrets: f.box, Tokens: tokens, Hostname: "box.example.test", UnsubscribeURL: "https://panel.example.test/api/v1/marketing/unsubscribe", Content: func(context.Context, int64, int64, string) (string, string, bool, error) {
		return "Welcome", "Consented flow message", true, nil
	}}
}

// A suppression recorded before the flow send marks the whole position
// unsubscribed: this step and every later step stop without contacting SMTP.
func TestFlowSenderSuppressionIsTerminalForAllSteps(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	email := "recipient-0@example.test"
	flowID := flowFixture(t, f, email)
	if _, err := store.New(f.platform.Pool).AddMarketingSuppression(context.Background(), store.AddMarketingSuppressionParams{TenantID: f.tenant, Email: email, Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	if err := flowSender(t, f).Send(context.Background(), f.tenant, flowID, email, "welcome"); err != nil {
		t.Fatal(err)
	}
	var unsubscribed bool
	if err := f.platform.QueryRow(context.Background(), "SELECT unsubscribed FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2", f.tenant, flowID).Scan(&unsubscribed); err != nil || !unsubscribed {
		t.Fatalf("flow position not terminal: %t %v", unsubscribed, err)
	}
}

// An entitlement loss skips the send without unsubscribing the position, so
// the flow driver may resume it after the grace window closes and reopens.
func TestFlowSenderEntitlementLossSkipsWithoutUnsubscribing(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	email := "recipient-0@example.test"
	flowID := flowFixture(t, f, email)
	if _, err := f.platform.Exec(context.Background(), "UPDATE subscription_terms SET plan_snapshot=plan_snapshot-'marketing_daily_recipient_limit'-'marketing_rate_per_minute' WHERE tenant_id=$1", f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.platform.Exec(context.Background(), "UPDATE subscriptions SET plan_snapshot=NULL WHERE tenant_id=$1", f.tenant); err != nil {
		t.Fatal(err)
	}
	err := flowSender(t, f).Send(context.Background(), f.tenant, flowID, email, "welcome")
	if !errors.Is(err, marketingflow.ErrNotAllowed) {
		t.Fatalf("expected retryable denial: %v", err)
	}
	var unsubscribed bool
	if err = f.platform.QueryRow(context.Background(), "SELECT unsubscribed FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2", f.tenant, flowID).Scan(&unsubscribed); err != nil || unsubscribed {
		t.Fatalf("position must stay subscribed: %t %v", unsubscribed, err)
	}
}

func TestFlowSenderRequiresActiveFlow(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	flowID := flowFixture(t, f, "recipient-0@example.test")
	if _, err := f.platform.Exec(context.Background(), "UPDATE marketing_flows SET status='paused' WHERE tenant_id=$1 AND id=$2", f.tenant, flowID); err != nil {
		t.Fatal(err)
	}
	err := flowSender(t, f).Send(context.Background(), f.tenant, flowID, "recipient-0@example.test", "welcome")
	if !errors.Is(err, marketingflow.ErrNotAllowed) {
		t.Fatalf("paused flow sent: %v", err)
	}
}

func TestFlowSenderRequiresStepContent(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	flowID := flowFixture(t, f, "recipient-0@example.test")
	sender := flowSender(t, f)
	sender.Content = func(context.Context, int64, int64, string) (string, string, bool, error) { return "", "", false, nil }
	err := sender.Send(context.Background(), f.tenant, flowID, "recipient-0@example.test", "welcome")
	if err == nil || !strings.Contains(err.Error(), "no content") {
		t.Fatalf("missing content accepted: %v", err)
	}
}

package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
)

type rejectionRecorderFake struct {
	params   []store.PlatformRecordRejectedWebhookParams
	recorded bool
	err      error
}

func (f *rejectionRecorderFake) PlatformRecordRejectedWebhook(ctx context.Context, params store.PlatformRecordRejectedWebhookParams) (bool, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
		return false, errors.New("diagnostic persistence has no bounded deadline")
	}
	f.params = append(f.params, params)
	return f.recorded, f.err
}

func TestRejectedWebhookStoresOnlyFingerprintAndLength(t *testing.T) {
	raw := []byte(`{"secret":"a callback body must never enter diagnostics"}`)
	f := &rejectionRecorderFake{recorded: true}
	RecordRejectedWebhook(context.Background(), f, 42, "verification_rejected", raw)
	if len(f.params) != 1 {
		t.Fatalf("expected metadata once, got %d", len(f.params))
	}
	digest := sha256.Sum256(raw)
	got := f.params[0]
	if got.PayloadHash != hex.EncodeToString(digest[:]) || int(got.PayloadBytes) != len(raw) || got.MerchantID != 42 || got.Reason != "verification_rejected" {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	// Invalid metadata never reaches a persistence implementation.
	RecordRejectedWebhook(context.Background(), f, 0, "verification_rejected", raw)
	RecordRejectedWebhook(context.Background(), f, 42, "caller supplied reason", raw)
	RecordRejectedWebhook(context.Background(), f, 42, "merchant_rejected", []byte(strings.Repeat("x", (1<<20)+1)))
	if len(f.params) != 1 {
		t.Fatal("invalid metadata reached the database")
	}
}

func TestRejectedWebhookDiagnosticFailureDoesNotEscape(t *testing.T) {
	for _, f := range []*rejectionRecorderFake{{recorded: false}, {err: errors.New("database unavailable")}} {
		RecordRejectedWebhook(context.Background(), f, 42, "merchant_rejected", []byte("not signed"))
		if len(f.params) != 1 {
			t.Fatal("expected a single best-effort metadata operation")
		}
	}
}

package billingrenew

import (
	"context"
	"errors"
	"testing"
)

// Listing persisted hosted intents is read-only: a missing charging channel
// (development, or before provider onboarding) must return an empty list, not
// 409 ADAPTER_UNAVAILABLE, because browsers surface any 4xx as console errors.
func TestListHostedSetupsWithoutChargingChannelReturnsEmpty(t *testing.T) {
	service := EnrollmentService{}
	rows, err := service.ListHostedSetups(context.Background(), 1, 1, "")
	if err != nil {
		t.Fatalf("empty service list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty list, got %d rows", len(rows))
	}
	service = EnrollmentService{Dispatcher: &Dispatcher{}}
	rows, err = service.ListHostedSetups(context.Background(), 1, 1, "")
	if err != nil {
		t.Fatalf("pool-less dispatcher list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty list, got %d rows", len(rows))
	}
	if !errors.Is(setupAvailableOn(service), ErrNoChargingChannel) {
		t.Fatal("mutating paths must still refuse without a charging channel")
	}
}

// setupAvailableOn mirrors the internal gate so the test pins the contract
// that only ListHostedSetups bypasses it.
func setupAvailableOn(s EnrollmentService) error {
	return s.setupAvailable()
}

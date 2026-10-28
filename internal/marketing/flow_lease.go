package marketing

import "context"

type flowLeaseKey struct{}

// flowLease belongs to one claimed position and is never taken from HTTP input.
type flowLease struct {
	owner     string
	fence     int64
	confirmed bool
}

func leaseFromContext(ctx context.Context) *flowLease {
	lease, _ := ctx.Value(flowLeaseKey{}).(*flowLease)
	return lease
}

// flowGateError exposes a bounded operational reason, never SMTP credentials.
type flowGateError struct{ code string }

func (e *flowGateError) Error() string { return "marketing flow gate: " + e.code }

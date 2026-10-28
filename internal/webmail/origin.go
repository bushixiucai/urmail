package webmail

import "context"

type originHostKey struct{}

// WithOriginHost carries the server-observed HTTP host through native adapters.
func WithOriginHost(ctx context.Context, host string) context.Context {
	return context.WithValue(ctx, originHostKey{}, host)
}

// OriginHost returns only the server-supplied host, never a request payload field.
func OriginHost(ctx context.Context) string {
	value, _ := ctx.Value(originHostKey{}).(string)
	return value
}

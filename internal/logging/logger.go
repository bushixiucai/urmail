// Package logging builds the structured logger used across urMail.cn.
//
// A field denylist is enforced at the logger level: secrets and message bodies
// must never reach the log stream, regardless of what a call site passes.
package logging

import (
	"os"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// deniedFields never appear in log output. Matching is case-insensitive and
// also catches the obvious suffixed variants (password_hash, user_password).
var deniedFields = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"apikey",
	"api_key",
	"authorization",
	"cookie",
	"session",
	"totp",
	"mfa_secret",
	"body",
	"content",
	"message_body",
	"mail_body",
	"plaintext",
	"private_key",
	"master_key",
	"dsn",
	"credential",
}

var deniedOnce sync.Once
var deniedExact map[string]struct{}

// IsDenied reports whether a log field name is not allowed.
func IsDenied(name string) bool {
	deniedOnce.Do(func() {
		deniedExact = make(map[string]struct{}, len(deniedFields))
		for _, field := range deniedFields {
			deniedExact[field] = struct{}{}
		}
	})

	lowered := strings.ToLower(name)
	if _, ok := deniedExact[lowered]; ok {
		return true
	}
	for _, field := range deniedFields {
		if strings.Contains(lowered, field) {
			return true
		}
	}
	return false
}

// redactField drops a denied field, replacing it with a marker so that the
// presence of the field is still visible during debugging.
func redactField(field zapcore.Field) zapcore.Field {
	if !IsDenied(field.Key) {
		return field
	}
	return zap.String(field.Key, "[redacted]")
}

// New builds a JSON logger writing to stderr.
func New(level string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		zapLevel = zapcore.InfoLevel
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "ts"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.MessageKey = "msg"
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.Lock(os.Stderr),
		zapLevel,
	)

	logger := zap.New(redactCore{Core: core}, zap.AddCaller(), zap.ErrorOutput(zapcore.Lock(os.Stderr)))
	return logger, nil
}

// redactCore redacts denied fields on their way to the underlying core.
type redactCore struct {
	zapcore.Core
}

// With returns a core whose fields are redacted.
func (c redactCore) With(fields []zapcore.Field) zapcore.Core {
	redacted := make([]zapcore.Field, len(fields))
	for i, field := range fields {
		redacted[i] = redactField(field)
	}
	return redactCore{Core: c.Core.With(redacted)}
}

// Write redacts the entry fields before writing them out.
func (c redactCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	for i := range fields {
		fields[i] = redactField(fields[i])
	}
	return c.Core.Write(entry, fields)
}

// Check adds this wrapper — not the inner core — to the checked entry, so
// that the redaction in Write is actually applied. Delegating to the inner
// core's Check would register the inner core and bypass the wrapper.
func (c redactCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

// Enabled reports whether the underlying core accepts the given level.
func (c redactCore) Enabled(level zapcore.Level) bool {
	return c.Core.Enabled(level)
}

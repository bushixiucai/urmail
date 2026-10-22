package logging

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestIsDeniedCoversSecretsAndMessageBodies(t *testing.T) {
	denied := []string{
		"password",
		"Password",
		"user_password",
		"password_hash",
		"secret",
		"mfa_secret",
		"token",
		"access_token",
		"api_key",
		"apiKey",
		"authorization",
		"cookie",
		"session_id",
		"totp",
		"body",
		"content",
		"message_body",
		"private_key",
		"master_key",
		"dsn",
	}

	for _, name := range denied {
		if !IsDenied(name) {
			t.Errorf("field %q must be denied", name)
		}
	}
}

func TestIsDeniedAllowsOrdinaryFields(t *testing.T) {
	allowed := []string{
		"tenant_id",
		"mailbox_id",
		"request_id",
		"status",
		"duration",
		"client_ip",
		"op",
		"ok",
	}

	for _, name := range allowed {
		if IsDenied(name) {
			t.Errorf("field %q must be allowed", name)
		}
	}
}

func TestLoggerRedactsDeniedFields(t *testing.T) {
	output := captureOutput(t,
		func() *zap.Logger {
			logger, err := New("debug")
			if err != nil {
				t.Fatalf("build logger: %v", err)
			}
			return logger
		},
		func(logger *zap.Logger) {
			logger.Info("test", zapString("password", "hunter2"), zapString("tenant_id", "42"))
		},
	)

	if strings.Contains(output, "hunter2") {
		t.Fatalf("the password leaked into the log: %s", output)
	}
	if !strings.Contains(output, "[redacted]") {
		t.Fatalf("expected a redaction marker: %s", output)
	}
	if !strings.Contains(output, "42") {
		t.Fatalf("an ordinary field was dropped: %s", output)
	}
}

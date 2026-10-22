package logging

import (
	"bytes"
	"os"
	"testing"

	"go.uber.org/zap"
)

// zapString is a shorthand for a string log field.
func zapString(key, value string) zap.Field {
	return zap.String(key, value)
}

// captureOutput redirects stderr while the given function runs and returns
// everything that was written to it.
//
// The logger binds os.Stderr at construction time, so the redirection has to
// be in place before the logger is built. The caller therefore passes a
// builder rather than a ready-made logger.
func captureOutput(t *testing.T, build func() *zap.Logger, run func(*zap.Logger)) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}

	original := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = original }()

	logger := build()
	run(logger)
	_ = logger.Sync()

	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(reader); err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	return buffer.String()
}

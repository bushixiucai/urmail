package agentd_test

import (
	"io"
	"log/slog"
)

// discardLogger returns a logger that throws everything away, so that the
// tests do not pollute their output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

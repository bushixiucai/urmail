package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type trackedStream struct {
	reader io.Reader
	closed bool
	reads  int
}

func (r *trackedStream) Read(p []byte) (int, error) { r.reads++; return r.reader.Read(p) }
func (r *trackedStream) Close() error               { r.closed = true; return nil }

func TestDeferredStreamWaitsForPublicationAndBypassesJSONBuffer(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/archive/1/export", nil)
	buffer := newResponseBuffer(c.Writer)
	c.Writer = buffer
	source := &trackedStream{reader: bytes.NewReader(make([]byte, 9<<20))}
	if err := DeferStream(c, source, "message/rfc822", 9<<20); err != nil {
		t.Fatal(err)
	}
	if source.reads != 0 || recorder.Body.Len() != 0 || buffer.body.Len() != 0 {
		t.Fatal("stream escaped before commit publication")
	}
	if err := buffer.publishResponse(); err != nil {
		t.Fatal(err)
	}
	buffer.closeStream()
	if recorder.Body.Len() != 9<<20 || !source.closed || buffer.overflow || buffer.body.Len() != 0 {
		t.Fatal("large stream buffered, truncated or not closed")
	}
}

func TestDeferredStreamRollbackClosesWithoutReading(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/archive/1/export", nil)
	buffer := newResponseBuffer(c.Writer)
	c.Writer = buffer
	source := &trackedStream{reader: bytes.NewBufferString("private")}
	if err := DeferStream(c, source, "message/rfc822", 7); err != nil {
		t.Fatal(err)
	}
	c.AbortWithStatusJSON(500, gin.H{"error": "audit_failed"})
	if err := buffer.publishResponse(); err != nil {
		t.Fatal(err)
	}
	buffer.closeStream()
	if source.reads != 0 || !source.closed || bytes.Contains(recorder.Body.Bytes(), []byte("private")) {
		t.Fatal("failed audit released private bytes")
	}
	if recorder.Header().Get("Content-Length") != "" {
		t.Fatal("failed stream retained private length")
	}
}

func TestDeferredStreamHonorsCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancel(t.Context())
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodGet, "/archive/1/export", nil)
	buffer := newResponseBuffer(c.Writer)
	c.Writer = buffer
	source := &trackedStream{reader: bytes.NewBufferString("private")}
	if err := DeferStream(c, source, "message/rfc822", 7); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := buffer.publishResponse(); err == nil {
		t.Fatal("canceled stream succeeded")
	}
	buffer.closeStream()
	if source.reads != 0 || !source.closed || recorder.Body.Len() != 0 {
		t.Fatal("canceled response leaked or retained stream")
	}
}

func TestAfterCommitStreamReplacementKeepsNewHeadersAndClosesOld(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/download", nil)
	buffer := newResponseBuffer(c.Writer)
	c.Writer = buffer
	original := &trackedStream{reader: bytes.NewBufferString("original")}
	replacement := &trackedStream{reader: bytes.NewBufferString("new")}
	if err := DeferStream(c, original, "message/rfc822", 8); err != nil {
		t.Fatal(err)
	}
	buffer.beginAfterCommitCallback()
	if err := DeferStream(c, replacement, "application/octet-stream", 3); err != nil {
		t.Fatal(err)
	}
	if !original.closed || original.reads != 0 {
		t.Fatal("old prepared stream was not discarded safely")
	}
	if err := buffer.publishResponse(); err != nil {
		t.Fatal(err)
	}
	buffer.closeStream()
	if recorder.Body.String() != "new" || recorder.Header().Get("Content-Type") != "application/octet-stream" || recorder.Header().Get("Content-Length") != "3" || !replacement.closed {
		t.Fatal("replacement lost headers, bytes or cleanup")
	}
}

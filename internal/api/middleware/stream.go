package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var deferredStreams = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_http_deferred_streams_total", Help: "Post-commit stream completion outcomes."}, []string{"result"})

type deferredStream struct {
	source io.ReadCloser
	size   int64
	ctx    context.Context
}

// DeferStream transfers ownership to the request transaction. No source byte is
// sent until that transaction and its successful audit commit. Rollback, panic,
// cancellation and failed commits all close the source without streaming it.
// Without transaction middleware, the caller must already have committed audit.
func DeferStream(c *gin.Context, source io.ReadCloser, contentType string, size int64) error {
	if source == nil || size < 0 {
		return errors.New("invalid deferred stream")
	}
	c.Status(http.StatusOK)
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	stream := &deferredStream{source: source, size: size, ctx: c.Request.Context()}
	if buffered, ok := c.Writer.(*responseBuffer); ok {
		if buffered.stream != nil || buffered.body.Len() != 0 {
			_ = source.Close()
			return errors.New("transaction already has a response body")
		}
		buffered.stream = stream
		return nil
	}
	defer func() { _ = source.Close() }()
	return stream.write(c.Writer)
}

type streamContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r streamContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
func (s *deferredStream) write(writer io.Writer) error {
	n, err := io.CopyN(writer, streamContextReader{s.ctx, s.source}, s.size)
	if err == nil && n != s.size {
		err = io.ErrUnexpectedEOF
	}
	result := "completed"
	if err != nil {
		result = "failed"
	}
	deferredStreams.WithLabelValues(result).Inc()
	return err
}
func (w *responseBuffer) closeStream() {
	if w.stream != nil {
		_ = w.stream.source.Close()
	}
}
func (w *responseBuffer) publishResponse() error {
	for k := range w.ResponseWriter.Header() {
		w.ResponseWriter.Header().Del(k)
	}
	for k, v := range w.header {
		w.ResponseWriter.Header()[k] = v
	}
	w.ResponseWriter.WriteHeader(w.status)
	if w.stream != nil && w.status < 400 && !w.overflow {
		return w.stream.write(w.ResponseWriter)
	}
	_, err := w.ResponseWriter.Write(w.body.Bytes())
	return err
}

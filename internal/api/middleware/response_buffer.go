package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

const contextAfterCommit = "urmail.after_commit"
const contextTransaction = "urmail.transaction"

// AfterCommit schedules a side effect only after the request transaction commits.
// Without a request transaction the caller has already committed its own work.
func AfterCommit(c *gin.Context, fn func()) {
	if _, ok := c.Get(contextTransaction); !ok {
		fn()
		return
	}
	callbacks, _ := c.Get(contextAfterCommit)
	list, _ := callbacks.([]func())
	c.Set(contextAfterCommit, append(list, fn))
}

type responseBuffer struct {
	gin.ResponseWriter
	header         http.Header
	body           bytes.Buffer
	status         int
	written        bool
	overflow       bool
	replaceOnWrite bool
	stream         *deferredStream
}

func newResponseBuffer(w gin.ResponseWriter) *responseBuffer {
	return &responseBuffer{ResponseWriter: w, header: w.Header().Clone(), status: 200}
}
func (w *responseBuffer) Header() http.Header { return w.header }
func (w *responseBuffer) WriteHeader(status int) {
	w.prepareCallbackWrite()
	if status >= 400 && w.stream != nil {
		w.closeStream()
		w.stream = nil
		w.header.Del("Content-Length")
		w.header.Del("Content-Type")
	}
	if !w.written {
		w.status = status
	}
}
func (w *responseBuffer) WriteHeaderNow() { w.written = true }
func (w *responseBuffer) Write(p []byte) (int, error) {
	w.prepareCallbackWrite()
	if w.stream != nil {
		w.overflow = true
		return 0, errors.New("transaction already has a deferred stream")
	}
	w.written = true
	if w.body.Len()+len(p) > 8<<20 {
		w.overflow = true
		return 0, errors.New("transaction response exceeds 8 MiB")
	}
	return w.body.Write(p)
}
func (w *responseBuffer) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *responseBuffer) Status() int                       { return w.status }
func (w *responseBuffer) Size() int {
	if !w.written {
		return -1
	}
	return w.body.Len()
}
func (w *responseBuffer) Written() bool { return w.written }
func (w *responseBuffer) Flush()        { w.WriteHeaderNow() }
func (w *responseBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijacking a transaction response is not supported")
}
func (w *responseBuffer) Pusher() http.Pusher { return nil }
func (w *responseBuffer) publish()            { _ = w.publishResponse() }

// A callback can replace a prepared response after commit without appending a
// second JSON document or hiding its final error status behind an earlier 200.
func (w *responseBuffer) beginAfterCommitCallback() { w.replaceOnWrite = true }
func (w *responseBuffer) prepareCallbackWrite() {
	if w.replaceOnWrite {
		if w.stream != nil {
			w.closeStream()
			w.stream = nil
			w.header.Del("Content-Length")
			w.header.Del("Content-Type")
		}
		w.body.Reset()
		w.written = false
		w.overflow = false
		w.replaceOnWrite = false
	}
}

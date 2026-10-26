package middleware_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api"
	"github.com/bushixiucai/urmail/internal/api/middleware"
)

func TestMailboxBodyLimitOverridesAreExact(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	for _, tc := range []struct {
		method, path string
		limit        int64
	}{
		{http.MethodPost, "/api/v1/mailbox/send", 16 << 20},
		{http.MethodPut, "/api/v1/mailbox/drafts", 16 << 20},
		{http.MethodPost, "/api/v1/mailbox/drafts", 1 << 20},
		{http.MethodPut, "/api/v1/mailbox/send", 1 << 20},
		{http.MethodPost, "/api/v1/mailbox/scheduled-sends", 1 << 20},
		{http.MethodPost, "/api/v1/mailbox/session", 1 << 20},
		{http.MethodPost, "/api/v1/auth/login", 1 << 20},
		{http.MethodPost, "/api/v1/mailbox/send-extra", 1 << 20},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.BodyLimitWithMailboxUploads(1<<20, 16<<20))
			router.Handle(tc.method, tc.path, func(c *gin.Context) {
				read, err := io.Copy(io.Discard, c.Request.Body)
				var overflow *http.MaxBytesError
				if errors.As(err, &overflow) {
					if overflow.Limit != tc.limit {
						t.Errorf("limit=%d want=%d", overflow.Limit, tc.limit)
					}
					c.Status(http.StatusRequestEntityTooLarge)
					return
				}
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				if read != 2<<20 {
					t.Errorf("read=%d", read)
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, strings.NewReader(strings.Repeat("x", 2<<20)))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			want := http.StatusRequestEntityTooLarge
			if tc.limit == 16<<20 {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("status=%d want=%d", response.Code, want)
			}
		})
	}
}

func TestBodyLimitRetainsDefaultBehavior(t *testing.T) {
	router := gin.New()
	router.Use(middleware.BodyLimit(1 << 20))
	router.POST("/api/v1/mailbox/send", func(c *gin.Context) {
		_, err := io.Copy(io.Discard, c.Request.Body)
		var overflow *http.MaxBytesError
		if !errors.As(err, &overflow) || overflow.Limit != 1<<20 {
			t.Errorf("default limit bypassed: %v", err)
		}
		c.Status(http.StatusRequestEntityTooLarge)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/send", strings.NewReader(strings.Repeat("x", 2<<20))))
}

type mailboxCountingBody struct {
	reader io.Reader
	read   int
}

func (body *mailboxCountingBody) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	body.read += n
	return n, err
}
func (*mailboxCountingBody) Close() error { return nil }

func TestProductionRouterRejectsUnauthorizedUploadWithoutReading(t *testing.T) {
	router := api.New(api.Options{WebmailNative: true})
	for _, tc := range []struct{ method, path string }{{http.MethodPost, "/api/v1/mailbox/send"}, {http.MethodPut, "/api/v1/mailbox/drafts"}} {
		body := &mailboxCountingBody{reader: strings.NewReader(strings.Repeat("x", 2<<20))}
		request := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, body)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", tc.path, response.Code, response.Body.String())
		}
		if body.read != 0 {
			t.Fatalf("%s consumed %d unauthorized body bytes", tc.path, body.read)
		}
	}
	// Added routes inherit the same production middleware chain.
	router.POST("/api/v1/bodylimit-probe", func(c *gin.Context) {
		_, err := io.Copy(io.Discard, c.Request.Body)
		var overflow *http.MaxBytesError
		if !errors.As(err, &overflow) || overflow.Limit != 1<<20 {
			t.Errorf("production default limit bypassed: %v", err)
		}
		c.Status(http.StatusRequestEntityTooLarge)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/bodylimit-probe", strings.NewReader(strings.Repeat("x", 2<<20))))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default route status=%d", response.Code)
	}
}

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type fakeEvidence struct{ visible bool }

func (f fakeEvidence) PlatformEvidenceVisible(context.Context, store.PlatformEvidenceVisibleParams) (bool, error) {
	return f.visible, nil
}
func (f fakeEvidence) PlatformReadEvidenceBody(context.Context, store.PlatformReadEvidenceBodyParams) ([]byte, error) {
	return nil, nil
}

func TestEvidenceDownloadRejectsHiddenManifest(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	reads := 0
	h := ArchiveEvidence{Q: fakeEvidence{visible: false}, Manifest: func(context.Context, int64, int64) (string, error) { reads++; return "manifest", nil }}
	r := gin.New()
	r.GET("/api/v1/archive/evidence/:id", func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, int64(7))
		c.Set(middleware.ContextUserID, int64(9))
		h.Download(c)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/archive/evidence/4?digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

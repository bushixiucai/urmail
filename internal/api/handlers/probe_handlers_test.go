package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// Explicit adapter belongs only in tests: production must never fall back to a
// bare DomainStore when the short-transaction capability has not been wired.
type domainStoreProbeFake struct{ store DomainStore }

func (f domainStoreProbeFake) ReadVerification(ctx context.Context, a ProbeActor, id int64) (store.GetDomainByIDRow, error) {
	return f.store.GetDomainByID(ctx, store.GetDomainByIDParams{TenantID: a.TenantID, ID: id})
}
func (f domainStoreProbeFake) CompleteVerification(ctx context.Context, a ProbeActor, s store.GetDomainByIDRow) (store.VerifyDomainChallengeRow, error) {
	return f.store.VerifyDomainChallenge(ctx, store.VerifyDomainChallengeParams{TenantID: a.TenantID, ID: s.ID, VerificationToken: s.VerificationToken})
}

type healthProbeFake struct {
	mu         sync.Mutex
	active     bool
	version    int
	authorized bool
	writes     int
}

func (f *healthProbeFake) ReadHealth(context.Context, ProbeActor) (AIHealthSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = true
	defer func() { f.active = false }()
	return AIHealthSnapshot{Present: true, Configuration: store.AiProviderConfigs{Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "test", Enabled: true, ConfigurationVersion: int64(f.version)}}, nil
}
func (f *healthProbeFake) CompleteHealth(_ context.Context, _ ProbeActor, s AIHealthSnapshot, _ bool, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = true
	defer func() { f.active = false }()
	if !f.authorized {
		return ErrProbeForbidden
	}
	if s.Configuration.ConfigurationVersion != int64(f.version) {
		return ErrProbeChanged
	}
	f.writes++
	return nil
}
func probeRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, int64(1))
		c.Set(middleware.ContextUserID, int64(2))
		c.Next()
	})
	r.POST("/", handler)
	return r
}
func TestAIHealthReleasesReadBeforeSlowNetworkAndRejectsStaleResult(t *testing.T) {
	for _, change := range []string{"none", "version", "authorization"} {
		t.Run(change, func(t *testing.T) {
			repo := &healthProbeFake{version: 1, authorized: true}
			entered, release := make(chan struct{}), make(chan struct{})
			h := &AIConfigurationHandler{HealthRepository: repo, LocalEndpoints: []string{"http://127.0.0.1:11434"}}
			h.FetchHealthModels = func(ctx context.Context, _ ai.Provider) ([]string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second {
					t.Error("missing ten-second network budget")
				}
				repo.mu.Lock()
				active := repo.active
				repo.mu.Unlock()
				if active {
					t.Error("network invoked while repository transaction active")
				}
				close(entered)
				<-release
				return []string{"test"}, nil
			}
			r := probeRouter(h.Health)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("{}")))
				done <- w
			}()
			<-entered
			repo.mu.Lock()
			if repo.active {
				t.Error("transaction held during blocked network")
			}
			if change == "version" {
				repo.version++
			}
			if change == "authorization" {
				repo.authorized = false
			}
			repo.mu.Unlock()
			close(release)
			w := <-done
			want := 200
			if change == "version" {
				want = 409
			}
			if change == "authorization" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("status=%d want=%d %s", w.Code, want, w.Body.String())
			}
			if change != "none" && repo.writes != 0 {
				t.Fatal("stale result wrote state/audit")
			}
		})
	}
}

type blockedDomainProbe struct {
	domainStoreProbeFake
	completeErr error
	writes      int
}

func (f *blockedDomainProbe) CompleteVerification(ctx context.Context, a ProbeActor, s store.GetDomainByIDRow) (store.VerifyDomainChallengeRow, error) {
	if f.completeErr != nil {
		return store.VerifyDomainChallengeRow{}, f.completeErr
	}
	f.writes++
	return f.domainStoreProbeFake.CompleteVerification(ctx, a, s)
}
func TestDomainVerifyRejectsConcurrentVersionOrAuthorizationChange(t *testing.T) {
	for _, change := range []error{nil, ErrProbeChanged, ErrProbeForbidden} {
		fake := &shapeFakeDomainStore{byID: store.GetDomainByIDRow{ID: 1, TenantID: 1, Fqdn: "example.test", VerificationToken: "proof", Status: "pending_dns", VerificationIssuedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}, VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}}
		repo := &blockedDomainProbe{domainStoreProbeFake: domainStoreProbeFake{fake}}
		entered, release := make(chan struct{}), make(chan struct{})
		h := &DomainHandler{Verification: repo, LookupTXT: func(ctx context.Context, _ string) ([]string, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("no DNS deadline")
			}
			close(entered)
			<-release
			return []string{"proof"}, nil
		}}
		r := probeRouter(func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: "1"}}; h.Verify(c) })
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil))
			done <- w
		}()
		<-entered
		repo.completeErr = change
		close(release)
		w := <-done
		want := 200
		if errors.Is(change, ErrProbeChanged) {
			want = 409
		}
		if errors.Is(change, ErrProbeForbidden) {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("status=%d want=%d %s", w.Code, want, w.Body.String())
		}
		if change != nil && repo.writes != 0 {
			t.Fatal("stale DNS result written")
		}
	}
}
func TestProbesFailClosedWithoutCapability(t *testing.T) {
	for _, h := range []gin.HandlerFunc{(&DomainHandler{}).Verify, (&AIConfigurationHandler{}).Health} {
		r := probeRouter(h)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil))
		if w.Code != 503 {
			t.Fatalf("unwired status=%d", w.Code)
		}
	}
}

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestProbeRepositoriesReleaseRealTransactionsAndRevalidate(t *testing.T) {
	p, principal := mailboxAIFixture(t, 100)
	seatAIPlatformBudget(t, p, principal.TenantID)
	ctx := context.Background()
	var uid int64
	if err := p.QueryRow(ctx, "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner'", principal.TenantID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	actor := ProbeActor{TenantID: principal.TenantID, UserID: uid}
	app, err := db.OpenApp(ctx, p.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	domainRepo := NewDomainVerificationRepository(app)
	aiRepo := NewAIHealthRepository(p)
	var domainID int64
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token) VALUES($1,'probe.example.test','pending_dns','proof') RETURNING id", actor.TenantID).Scan(&domainID); err != nil {
		t.Fatal(err)
	}

	assertReleased := func() {
		t.Helper()
		if n := app.Stat().AcquiredConns(); n != 0 {
			t.Fatalf("app connections held during network: %d", n)
		}
		if n := p.Stat().AcquiredConns(); n != 0 {
			t.Fatalf("platform connections held during network: %d", n)
		}
		var active int
		if e := p.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND xact_start IS NOT NULL").Scan(&active); e != nil {
			t.Fatal(e)
		}
		if active != 0 {
			t.Fatalf("transactions still active during network: %d", active)
		}
		tx, e := app.UserTx(ctx, actor.TenantID, actor.UserID)
		if e != nil {
			t.Fatal(e)
		}
		var locked bool
		if e = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", actor.TenantID).Scan(&locked); e != nil {
			t.Fatal(e)
		}
		_ = tx.Rollback(ctx)
		if !locked {
			t.Fatal("probe retained tenant lock across network")
		}
	}
	for _, kind := range []string{"domain", "ai"} {
		for _, change := range []string{"none", "version", "authorization"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				if _, e := p.Exec(ctx, "UPDATE user_tenant_roles SET role='owner' WHERE tenant_id=$1 AND user_id=$2", actor.TenantID, actor.UserID); e != nil {
					t.Fatal(e)
				}
				var auditBefore int
				if e := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1", actor.TenantID).Scan(&auditBefore); e != nil {
					t.Fatal(e)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				network := func(c context.Context) {
					if deadline, ok := c.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
						t.Error("network timeout missing")
					}
					close(entered)
					<-release
				}
				var handler gin.HandlerFunc
				if kind == "domain" {
					if _, e := p.Exec(ctx, "UPDATE domains SET status='pending_dns',verified_at=NULL,verification_token='proof' WHERE tenant_id=$1 AND id=$2", actor.TenantID, domainID); e != nil {
						t.Fatal(e)
					}
					h := &DomainHandler{Verification: domainRepo, LookupTXT: func(c context.Context, _ string) ([]string, error) { network(c); return []string{"proof"}, nil }}
					handler = func(c *gin.Context) {
						c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(domainID, 10)}}
						h.Verify(c)
					}
				} else {
					h := &AIConfigurationHandler{HealthRepository: aiRepo, LocalEndpoints: []string{"http://127.0.0.1:11434"}, FetchHealthModels: func(c context.Context, _ ai.Provider) ([]string, error) { network(c); return []string{"test"}, nil }}
					handler = h.Health
				}
				r := gin.New()
				r.Use(func(c *gin.Context) {
					c.Set(middleware.ContextTenantID, actor.TenantID)
					c.Set(middleware.ContextUserID, actor.UserID)
					c.Next()
				})
				r.POST("/", handler)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					w := httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"provider":"ollama","endpoint":"http://127.0.0.1:11434","model":"test"}`)))
					done <- w
				}()
				<-entered
				assertReleased()
				switch change {
				case "version":
					var e error
					if kind == "domain" {
						_, e = p.Exec(ctx, "UPDATE domains SET verification_token='rotated' WHERE tenant_id=$1 AND id=$2", actor.TenantID, domainID)
					} else {
						_, e = p.Exec(ctx, "UPDATE ai_provider_configs SET updated_at=clock_timestamp() WHERE tenant_id=$1", actor.TenantID)
					}
					if e != nil {
						t.Fatal(e)
					}
				case "authorization":
					if _, e := p.Exec(ctx, "UPDATE user_tenant_roles SET role='member' WHERE tenant_id=$1 AND user_id=$2", actor.TenantID, actor.UserID); e != nil {
						t.Fatal(e)
					}
				}
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
					t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
				var auditAfter int
				if e := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1", actor.TenantID).Scan(&auditAfter); e != nil {
					t.Fatal(e)
				}
				wantAudit := auditBefore
				if change == "none" {
					wantAudit++
				}
				if auditAfter != wantAudit {
					t.Fatalf("audit count=%d want=%d", auditAfter, wantAudit)
				}
				if kind == "domain" && change != "none" {
					row, e := store.New(p).GetDomainByID(ctx, store.GetDomainByIDParams{TenantID: actor.TenantID, ID: domainID})
					if e != nil {
						t.Fatal(e)
					}
					if row.Status == "active" {
						t.Fatal("stale DNS activated domain")
					}
				}
			})
		}
	}
	if _, e := p.Exec(ctx, "UPDATE user_tenant_roles SET role='owner' WHERE tenant_id=$1 AND user_id=$2", actor.TenantID, actor.UserID); e != nil {
		t.Fatal(e)
	}
	approveMailboxAICloud(t, p, actor.TenantID, true)
	snapshot, e := aiRepo.ReadHealth(ctx, actor)
	if e != nil {
		t.Fatal(e)
	}
	if !snapshot.CloudAuthorized {
		t.Fatal("fixture has no cloud consent")
	}
	approveMailboxAICloud(t, p, actor.TenantID, false)
	if e = aiRepo.CompleteHealth(ctx, actor, snapshot, false, "reachable"); !errors.Is(e, ErrProbeChanged) && !errors.Is(e, ErrProbeForbidden) {
		t.Fatalf("revoked consent accepted: %v", e)
	}
	// The callback receives the same bounded context as begin/role/commit,
	// rather than accidentally capturing the unbounded request context.
	if e = (probeRepository{pool: p}).transaction(ctx, actor, func(boundedCtx context.Context, _ *store.Queries) error {
		deadline, ok := boundedCtx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("query phase did not receive three-second transaction budget")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// The concrete repository rejects missing tenant/user before opening a tx.
	if _, e := aiRepo.ReadHealth(ctx, ProbeActor{}); !errors.Is(e, ErrProbeForbidden) {
		t.Fatalf("unscoped read: %v", e)
	}
}

package handlers

// MAIL-004 boundary tests for alias targets. A target domain may be typed as a
// U-label and must be stored in the A-label wire form; a non-ASCII local part
// is the explicit EAI capability rejection (never a silent rewrite).

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type idnAliasStore struct {
	shapeFakeAliasStore
	cycleArg     store.AliasWouldCycleParams
	createCalled bool
}

func (f *idnAliasStore) AliasWouldCycle(_ context.Context, arg store.AliasWouldCycleParams) (bool, error) {
	f.cycleArg = arg
	return false, nil
}

func (f *idnAliasStore) CreateAlias(_ context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error) {
	f.createCalled = true
	f.createParams = arg
	return store.MailboxAliases{
		ID: 21, TenantID: arg.TenantID, DomainID: arg.DomainID, LocalPart: arg.LocalPart,
		Target: arg.Target, Kind: arg.Kind, Active: arg.Active, CreatedAt: shapeTime(),
	}, nil
}

func newIDNAliasRouter(fake *idnAliasStore) *gin.Engine {
	h := NewAliasHandler(fake, nil)
	return newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/aliases", h.Create) })
}

func idnAliasStoreFixture() *idnAliasStore {
	return &idnAliasStore{shapeFakeAliasStore: shapeFakeAliasStore{
		domain: store.GetDomainByIDRow{ID: 3, TenantID: shapeTenantID, Fqdn: "example.com", Status: "active"},
	}}
}

func TestAliasCreateStoresUnicodeTargetDomainAsALabel(t *testing.T) {
	fake := idnAliasStoreFixture()
	router := newIDNAliasRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/aliases",
		`{"domain_id":3,"local_part":"sales","target":"User@`+chineseSLDULabel+`","kind":"forward"}`)
	if code != http.StatusCreated {
		t.Fatalf("Unicode target rejected: %d %s", code, raw)
	}
	want := "user@" + chineseSLDALabel
	if fake.createParams.Target != want {
		t.Fatalf("stored target = %q; want %q", fake.createParams.Target, want)
	}
	// The cross-tenant cycle preflight must search the same wire form that is
	// stored, otherwise a Unicode spelling could hide an edge from the graph.
	if fake.cycleArg.Target != want {
		t.Fatalf("cycle preflight target = %q; want %q", fake.cycleArg.Target, want)
	}
	alias := shapeObject(t, body, "alias")
	if alias["target"] != want {
		t.Fatalf("response target = %v; want %q", alias["target"], want)
	}
}

func TestAliasCreateRejectsEAILocalPart(t *testing.T) {
	fake := idnAliasStoreFixture()
	router := newIDNAliasRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/aliases",
		`{"domain_id":3,"local_part":"sales","target":"用户@example.com","kind":"forward"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("EAI target status = %d body %s; want 400", code, raw)
	}
	errBody := shapeObject(t, body, "error")
	if errBody["code"] != "EAI_NOT_SUPPORTED" {
		t.Fatalf("code = %v; want EAI_NOT_SUPPORTED (%s)", errBody["code"], raw)
	}
	if errBody["message"] != "mailbox.eai_not_supported" {
		t.Fatalf("message = %v; want mailbox.eai_not_supported", errBody["message"])
	}
	if fake.createCalled {
		t.Fatalf("EAI target reached storage: %+v", fake.createParams)
	}
}

func TestAliasCreateStillRejectsMalformedTarget(t *testing.T) {
	fake := idnAliasStoreFixture()
	router := newIDNAliasRouter(fake)

	for _, target := range []string{"not-an-address", "user@@example.com", "bad*glob@example.com"} {
		code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/aliases",
			`{"domain_id":3,"local_part":"sales","target":"`+target+`","kind":"forward"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("target %q status = %d body %s; want 400", target, code, raw)
		}
		if errBody := shapeObject(t, body, "error"); errBody["code"] != "INVALID_ALIAS" {
			t.Fatalf("target %q code = %v; want the existing INVALID_ALIAS contract", target, errBody["code"])
		}
	}
	if fake.createCalled {
		t.Fatal("malformed target reached storage")
	}
}

type idnAliasUpdateStore struct {
	shapeFakeAliasStore
	existing     store.GetAliasByIDRow
	updated      store.UpdateAliasParams
	updateCalled bool
}

func (f *idnAliasUpdateStore) GetAliasByID(_ context.Context, _ store.GetAliasByIDParams) (store.GetAliasByIDRow, error) {
	return f.existing, nil
}

func (f *idnAliasUpdateStore) UpdateAlias(_ context.Context, arg store.UpdateAliasParams) (store.MailboxAliases, error) {
	f.updateCalled = true
	f.updated = arg
	return store.MailboxAliases{
		ID: arg.ID, TenantID: arg.TenantID, DomainID: 3, LocalPart: arg.LocalPart,
		Target: arg.Target, Kind: arg.Kind, Active: arg.Active, CreatedAt: shapeTime(),
	}, nil
}

// newIDNQueriesRouter mirrors the production request scope: the tenant id plus
// the request-scoped queries that tenant handlers read via requestStore.
func newIDNQueriesRouter(tenantID int64, queries any, register func(*gin.RouterGroup)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenantID)
		c.Set(middleware.ContextQueries, queries)
		c.Next()
	})
	register(group)
	return r
}

func idnAliasUpdateFixture() *idnAliasUpdateStore {
	return &idnAliasUpdateStore{
		existing: store.GetAliasByIDRow{
			ID: 5, TenantID: shapeTenantID, DomainID: 3, DomainFqdn: "example.com",
			LocalPart: "sales", Target: "old@example.com", Kind: "forward", Active: true,
			CreatedAt: pgtype.Timestamptz{},
		},
	}
}

func TestAliasUpdateNormalizesUnicodeTargetDomain(t *testing.T) {
	fake := idnAliasUpdateFixture()
	h := NewAliasHandler(nil, nil)
	router := newIDNQueriesRouter(shapeTenantID, fake, func(g *gin.RouterGroup) { g.PATCH("/aliases/:id", h.Update) })

	code, raw, _ := shapeRequest(t, router, http.MethodPatch, "/api/v1/aliases/5",
		`{"target":"User@`+chineseSLDULabel+`"}`)
	if code != http.StatusOK {
		t.Fatalf("Unicode target update rejected: %d %s", code, raw)
	}
	want := "user@" + chineseSLDALabel
	if !fake.updateCalled || fake.updated.Target != want {
		t.Fatalf("updated target = %q; want %q", fake.updated.Target, want)
	}
}

func TestAliasUpdateRejectsEAILocalPart(t *testing.T) {
	fake := idnAliasUpdateFixture()
	h := NewAliasHandler(nil, nil)
	router := newIDNQueriesRouter(shapeTenantID, fake, func(g *gin.RouterGroup) { g.PATCH("/aliases/:id", h.Update) })

	code, raw, body := shapeRequest(t, router, http.MethodPatch, "/api/v1/aliases/5", `{"target":"用户@example.com"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("EAI target update status = %d body %s; want 400", code, raw)
	}
	if errBody := shapeObject(t, body, "error"); errBody["code"] != "EAI_NOT_SUPPORTED" {
		t.Fatalf("code = %v; want EAI_NOT_SUPPORTED (%s)", errBody["code"], raw)
	}
	if fake.updateCalled {
		t.Fatal("EAI target reached storage")
	}
}

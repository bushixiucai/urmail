package handlers

// MAIL-004 boundary tests for IDN domains. The stored/rendered wire form is
// always the lowercase A-label; the U-label exists only as display_fqdn. These
// tests are hermetic (fake store, real gin router) and run without PostgreSQL;
// the same contract against the real schema is in domains_idn_integration_test.go.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/store"
)

const (
	chineseDomainULabel = "例子.中国"
	chineseDomainALabel = "xn--fsqu00a.xn--fiqs8s"
	chineseSLDULabel    = "例子.example"
	chineseSLDALabel    = "xn--fsqu00a.example"
)

// idnDomainStore is a stateful fake that enforces the production
// domains.fqdn UNIQUE constraint on the exact value the handler hands to
// storage. "One domain, one row" is therefore tested at the HTTP boundary, not
// only inside the normalizer.
type idnDomainStore struct {
	shapeFakeDomainStore
	rows map[string]int64
	seq  int64
}

func (f *idnDomainStore) CreateDomain(_ context.Context, arg store.CreateDomainParams) (store.CreateDomainRow, error) {
	if f.rows == nil {
		f.rows = make(map[string]int64)
	}
	if _, exists := f.rows[arg.Fqdn]; exists {
		return store.CreateDomainRow{}, &pgconn.PgError{
			Code:    "23505",
			Message: `duplicate key value violates unique constraint "domains_fqdn_key"`,
		}
	}
	f.seq++
	f.rows[arg.Fqdn] = f.seq
	return store.CreateDomainRow{
		ID:                f.seq,
		TenantID:          arg.TenantID,
		Fqdn:              arg.Fqdn,
		Mode:              arg.Mode,
		Status:            arg.Status,
		DkimSelector:      arg.DkimSelector,
		CreatedAt:         shapeTime(),
		ProvisioningState: "pending",
	}, nil
}

func newIDNDomainRouter(fake *idnDomainStore) *gin.Engine {
	h := NewDomainHandler(fake, nil)
	return newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) {
		g.GET("/domains", h.List)
		g.POST("/domains", h.Create)
	})
}

func TestDomainCreateStoresUnicodeInputAsALabel(t *testing.T) {
	fake := &idnDomainStore{}
	router := newIDNDomainRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":"`+chineseDomainULabel+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("Unicode domain rejected: %d %s", code, raw)
	}
	if _, ok := fake.rows[chineseDomainALabel]; !ok {
		t.Fatalf("stored rows = %v; want the A-label %q", fake.rows, chineseDomainALabel)
	}

	domain := shapeObject(t, body, "domain")
	if domain["fqdn"] != chineseDomainALabel {
		t.Fatalf("fqdn = %v; want the A-label storage form %q", domain["fqdn"], chineseDomainALabel)
	}
	if domain["display_fqdn"] != chineseDomainULabel {
		t.Fatalf("display_fqdn = %v; want the U-label %q", domain["display_fqdn"], chineseDomainULabel)
	}
	guidance := shapeObject(t, body, "dns_guidance")
	if want := "_urmail-verify." + chineseDomainALabel; guidance["verification_name"] != want {
		t.Fatalf("verification_name = %v; want %q", guidance["verification_name"], want)
	}
}

// TestDomainCreateRejectsSecondSpellingOfSameDomain proves that no two
// spellings of one domain can create two resources: the first spelling wins
// and every other spelling of the same name collides on the normalized value.
func TestDomainCreateRejectsSecondSpellingOfSameDomain(t *testing.T) {
	fake := &idnDomainStore{}
	router := newIDNDomainRouter(fake)

	spellings := []string{
		chineseDomainULabel,       // U-label
		chineseDomainALabel,       // canonical A-label
		"XN--FSQU00A.XN--FIQS8S",  // A-label, upper case
		"例子。中国",                   // U+3002 ideographic full stop
		chineseDomainULabel + ".", // trailing root dot
	}
	for i, fqdn := range spellings {
		code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":"`+fqdn+`"}`)
		if i == 0 {
			if code != http.StatusCreated {
				t.Fatalf("first spelling rejected: %d %s", code, raw)
			}
			continue
		}
		if code != http.StatusConflict {
			t.Fatalf("spelling %q created a second resource: %d %s", fqdn, code, raw)
		}
		if errBody := shapeObject(t, body, "error"); errBody["code"] != "DOMAIN_CONFLICT" {
			t.Fatalf("spelling %q returned %v; want DOMAIN_CONFLICT", fqdn, errBody["code"])
		}
	}
	if len(fake.rows) != 1 {
		t.Fatalf("stored %d rows for one domain: %v", len(fake.rows), fake.rows)
	}
	if _, ok := fake.rows[chineseDomainALabel]; !ok {
		t.Fatalf("the surviving row is not the A-label: %v", fake.rows)
	}
}

func TestDomainCreateRejectsInvalidDomainInput(t *testing.T) {
	fake := &idnDomainStore{}
	router := newIDNDomainRouter(fake)

	cases := []struct {
		name string
		fqdn string
	}{
		{"forged punycode", "xn--x.example"},
		{"all-digit punycode", "xn--00000000.example"},
		{"emoji encoded as A-label", "xn--e28h.example"},
		{"emoji U-label", "😀.example"},
		{"empty middle label", "a..b"},
		{"empty label in IDN", "例子..中国"},
		{"label over 63 octets", strings.Repeat("a", 64) + ".example"},
		{"leading hyphen", "-bad.example"},
		{"full email address", "user@" + chineseDomainULabel},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":"`+tc.fqdn+`"}`)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d body %s; want 400", code, raw)
			}
			errBody := shapeObject(t, body, "error")
			if errBody["code"] != "INVALID_FQDN" {
				t.Fatalf("code = %v; want INVALID_FQDN (%s)", errBody["code"], raw)
			}
			if errBody["message"] != "domain.invalid_fqdn" {
				t.Fatalf("message = %v; want domain.invalid_fqdn", errBody["message"])
			}
		})
	}
	if len(fake.rows) != 0 {
		t.Fatalf("invalid input reached storage: %v", fake.rows)
	}
}

// TestDomainCreateKeepsASCIIBehaviourUnchanged is the regression guard: an
// ASCII domain is still accepted, lowercased and stored exactly as before, and
// its display form is identical to the storage form.
func TestDomainCreateKeepsASCIIBehaviourUnchanged(t *testing.T) {
	fake := &idnDomainStore{}
	router := newIDNDomainRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":" Example.COM. ","mode":"managed"}`)
	if code != http.StatusCreated {
		t.Fatalf("ASCII domain rejected: %d %s", code, raw)
	}
	if _, ok := fake.rows["example.com"]; !ok {
		t.Fatalf("stored rows = %v; want example.com", fake.rows)
	}
	domain := shapeObject(t, body, "domain")
	if domain["fqdn"] != "example.com" || domain["display_fqdn"] != "example.com" || domain["mode"] != "managed" {
		t.Fatalf("unexpected ASCII domain payload: %v", domain)
	}

	code, raw, _ = shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":"example.com"}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate ASCII domain status = %d body %s; want 409", code, raw)
	}
}

// TestDomainListRendersUnicodeDisplayForm keeps list responses displayable:
// fqdn stays the stored A-label while display_fqdn carries the U-label.
func TestDomainListRendersUnicodeDisplayForm(t *testing.T) {
	fake := &idnDomainStore{shapeFakeDomainStore: shapeFakeDomainStore{domains: []store.ListDomainsByTenantRow{{
		ID: 9, TenantID: shapeTenantID, Fqdn: chineseDomainALabel, Mode: "managed",
		Status: "pending_dns", DkimSelector: "mail", CreatedAt: shapeTime(),
	}}}}
	router := newIDNDomainRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodGet, "/api/v1/domains", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, raw)
	}
	domains := shapeArray(t, body, "domains")
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain, got %v", domains)
	}
	domain := domains[0].(map[string]any)
	if domain["fqdn"] != chineseDomainALabel || domain["display_fqdn"] != chineseDomainULabel {
		t.Fatalf("unexpected presentation: %v", domain)
	}
}

// TestDomainCreateAcceptsAValidPunycodeLabel documents a deliberate boundary
// that upstream review labeled "forged": xn--hello is *valid* punycode whose
// U-label is 㗁㖼㖶, so RFC 5890 requires accepting it. Only A-labels whose
// punycode is invalid or decodes to a disallowed rune class are rejected
// (see internal/mail/validation/idna_test.go).
func TestDomainCreateAcceptsAValidPunycodeLabel(t *testing.T) {
	fake := &idnDomainStore{}
	router := newIDNDomainRouter(fake)

	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains", `{"fqdn":"xn--hello.example"}`)
	if code != http.StatusCreated {
		t.Fatalf("valid A-label rejected: %d %s", code, raw)
	}
	if _, ok := fake.rows["xn--hello.example"]; !ok {
		t.Fatalf("stored rows = %v; want xn--hello.example", fake.rows)
	}
	domain := shapeObject(t, body, "domain")
	if domain["fqdn"] != "xn--hello.example" {
		t.Fatalf("fqdn = %v; want the A-label", domain["fqdn"])
	}
	if domain["display_fqdn"] != "㗁㖼㖶.example" {
		t.Fatalf("display_fqdn = %v; want the decoded U-label", domain["display_fqdn"])
	}
}

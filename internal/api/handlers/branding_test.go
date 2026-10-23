package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

type publicBrandFixture struct {
	BrandingStore
	hosted store.PublicBrandByHostRow
	linked store.PublicBrandBySlugRow
}

func (s publicBrandFixture) PublicBrandByHost(_ context.Context, host string) (store.PublicBrandByHostRow, error) {
	if host == "authorized.example" {
		return s.hosted, nil
	}
	return store.PublicBrandByHostRow{}, pgx.ErrNoRows
}
func (s publicBrandFixture) PublicBrandBySlug(_ context.Context, slug string) (store.PublicBrandBySlugRow, error) {
	if slug == "linked" {
		return s.linked, nil
	}
	return store.PublicBrandBySlugRow{}, pgx.ErrNoRows
}

func TestPublicBrandHostWinsWithoutExposingSettings(t *testing.T) {
	st := publicBrandFixture{hosted: store.PublicBrandByHostRow{Slug: "hosted", Name: "Hosted", Theme: []byte(`{"primary":"#123456","database_url":"secret","background":"url(javascript:alert(1))"}`)}, linked: store.PublicBrandBySlugRow{Slug: "linked", Name: "Linked"}}
	r := gin.New()
	r.GET("/branding", NewBrandingHandler(st).Public)
	cases := []struct{ host, query, want, source string }{{"authorized.example:443", "linked", "hosted", "host"}, {"unknown.example", "linked", "linked", "link"}, {"unknown.example", "missing", "default", "default"}, {"unknown.example", "../linked", "default", "default"}}
	for _, tc := range cases {
		t.Run(tc.host+tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), "GET", "https://"+tc.host+"/branding?brand="+tc.query, nil)
			req.Header.Set("X-Forwarded-Host", "authorized.example")
			r.ServeHTTP(w, req)
			var body struct {
				Brand  PublicBrand `json:"brand"`
				Source string      `json:"source"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Brand.Slug != tc.want || body.Source != tc.source {
				t.Fatalf("brand=%s source=%s", body.Brand.Slug, body.Source)
			}
			if _, leak := body.Brand.Theme["database_url"]; leak {
				t.Fatal("public theme exposed unknown keys")
			}
			if _, injection := body.Brand.Theme["background"]; injection {
				t.Fatal("public theme accepted CSS URL")
			}
		})
	}
}

func TestBrandMutationValidationRejectsUnsafeInputs(t *testing.T) {
	for _, host := range []string{"user@evil.example", "evil.example/path", "evil.example:invalid", "../evil.example", "a.example\nX-Test: true"} {
		if _, ok := normalizeBrandHost(host); ok {
			t.Errorf("accepted unsafe host %q", host)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "data:image/svg+xml,<svg>", "//evil.example/a.png", "https://user:password@evil.example/logo"} {
		if safeVisualURL(raw) != nil {
			t.Errorf("accepted unsafe visual URL %q", raw)
		}
	}
	if _, valid := sanitizeBrandTheme([]byte(`{"primary":"url(https://evil.example)","evil":"red"}`)); valid {
		t.Fatal("unsafe theme accepted")
	}
	if _, valid := sanitizeBrandTheme([]byte(`[]`)); valid {
		t.Fatal("non-object theme accepted")
	}
	if clean, valid := sanitizeBrandTheme([]byte(`{"primary":"#123456","radius":"0.5rem"}`)); !valid || len(clean) != 2 {
		t.Fatal("valid theme rejected")
	}
}

func (s publicBrandFixture) PlatformPublicThemeSettings(context.Context, int64) ([]store.PlatformPublicThemeSettingsRow, error) {
	return nil, nil
}

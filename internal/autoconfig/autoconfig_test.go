package autoconfig

import (
	"context"
	"encoding/xml"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testConfig() Config {
	return Config{
		PrimaryMailHost: "box.urmail.cn",
		DomainHosts: map[string]string{
			"example.com": "mail.example.com",
		},
		POP3Enabled: true,
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\ngot:\n%swant:\n%s", name, got, want)
	}
}

// mustParseXML fails when a generated document is not well-formed XML,
// catching escaping regressions structurally.
func mustParseXML(t *testing.T, body string) {
	t.Helper()
	if err := xml.Unmarshal([]byte(body), new(any)); err != nil {
		t.Fatalf("generated document is not well-formed XML: %v\n%s", err, body)
	}
}

func TestMozillaAutoconfigXMLGolden(t *testing.T) {
	golden(t, "mozilla-pop3-enabled.xml", MozillaAutoconfigXML(testConfig(), "user@example.com"))
	cfg := testConfig()
	cfg.POP3Enabled = false
	golden(t, "mozilla-pop3-disabled.xml", MozillaAutoconfigXML(cfg, "user@example.com"))
}

func TestMozillaAutoconfigXMLEscaping(t *testing.T) {
	cfg := testConfig()
	cfg.DomainHosts[`evil.com`] = `h&ost<39>"x.com`
	email := `user@evil.com`
	got := MozillaAutoconfigXML(cfg, email)
	mustParseXML(t, got)
	if strings.Contains(got, `&ost`) || strings.Contains(got, `<39>`) {
		t.Fatalf("raw metacharacters leaked into XML:\n%s", got)
	}
	for _, want := range []string{"&amp;", "&lt;", "&gt;", "&quot;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected escaped entity %s in output:\n%s", want, got)
		}
	}
}

func TestMozillaAutoconfigXMLUnknownDomainFallback(t *testing.T) {
	got := MozillaAutoconfigXML(testConfig(), "someone@unconfigured.example.org")
	mustParseXML(t, got)
	if !strings.Contains(got, "<hostname>box.urmail.cn</hostname>") {
		t.Fatalf("unknown domain must fall back to primary mail host:\n%s", got)
	}
	if !strings.Contains(got, "id=\"unconfigured.example.org\"") {
		t.Fatalf("unknown domain should still be reflected as id:\n%s", got)
	}
	for _, email := range []string{"", "@", "no-at-sign", "@bad domain", "user@<script>"} {
		out := MozillaAutoconfigXML(testConfig(), email)
		mustParseXML(t, out)
		if !strings.Contains(out, "box.urmail.cn") {
			t.Fatalf("fallback host missing for %q", email)
		}
	}
}

func TestMozillaAutoconfigXMLDomainHostMap(t *testing.T) {
	got := MozillaAutoconfigXML(testConfig(), "user@EXAMPLE.com")
	if !strings.Contains(got, "<hostname>mail.example.com</hostname>") {
		t.Fatalf("domain host mapping not applied (case-insensitive):\n%s", got)
	}
}

func TestOutlookAutodiscoverXMLGolden(t *testing.T) {
	got := OutlookAutodiscoverXML(testConfig(), "user@example.com")
	mustParseXML(t, got)
	golden(t, "outlook-autodiscover.xml", got)
	for _, required := range []string{outlookResponseSchema, "<Type>IMAP</Type>", "<Port>993</Port>", "<Type>SMTP</Type>", "<Port>587</Port>", "<Encryption>TLS</Encryption>"} {
		if !strings.Contains(got, required) {
			t.Fatalf("supported transport missing %s: %s", required, got)
		}
	}
	if strings.Contains(got, "<Type>Exchange</Type>") || strings.Contains(got, "<Type>MobileSync</Type>") {
		t.Fatal("Outlook discovery advertised unsupported Exchange or wrong schema")
	}

}

func TestOutlookAutodiscoverXMLEscaping(t *testing.T) {
	got := OutlookAutodiscoverXML(testConfig(), "<x>&\"y@evil.example")
	mustParseXML(t, got)
	if !strings.Contains(got, "&lt;x&gt;&amp;") {
		t.Fatalf("LoginName not escaped:\n%s", got)
	}
}

// newRouter mounts the package on a fresh gin engine for routing tests.
func newRouter(t *testing.T, cfg Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mount(engine, cfg)
	return engine
}

func do(t *testing.T, engine *gin.Engine, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), method, target, nil))
	return w
}

func TestMountMozillaConfig(t *testing.T) {
	engine := newRouter(t, testConfig())
	w := do(t, engine, http.MethodGet, "/mail/config-v1.1.xml?emailaddress=user%40example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/xml; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), "mail.example.com") {
		t.Fatalf("unexpected body:\n%s", w.Body.String())
	}
}

func TestMountWellKnownRedirect(t *testing.T) {
	engine := newRouter(t, testConfig())
	w := do(t, engine, http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress=user%40example.com")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/mail/config-v1.1.xml?emailaddress=user%40example.com" {
		t.Fatalf("location = %q", loc)
	}
	w2 := do(t, engine, http.MethodGet, "/mail/config-v1.1.xml?emailaddress=user%40example.com")
	if w2.Code != http.StatusOK {
		t.Fatalf("redirect target status = %d", w2.Code)
	}
}

func TestMountAutodiscoverPost(t *testing.T) {
	engine := newRouter(t, testConfig())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/autodiscover/autodiscover.xml",
		strings.NewReader("emailaddress=user%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/xml; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	mustParseXML(t, w.Body.String())
	if !strings.Contains(w.Body.String(), "user@example.com") {
		t.Fatalf("email not reflected:\n%s", w.Body.String())
	}
}

func TestMountRateLimited(t *testing.T) {
	engine := newRouter(t, testConfig())
	var last int
	for i := 0; i < 40; i++ {
		last = do(t, engine, http.MethodGet, "/mail/config-v1.1.xml?emailaddress=a@b.example").Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected rate limit to kick in, last status = %d", last)
	}
}

func TestParseEmailDomain(t *testing.T) {
	cases := []struct {
		in     string
		domain string
		ok     bool
	}{
		{"user@example.com", "example.com", true},
		{"User@Example.COM", "example.com", true},
		{"a@b@c.example", "c.example", true}, // last @ wins
		{"", "", false},
		{"@example.com", "", false},
		{"user@", "", false},
		{"no-at-sign", "", false},
		{"user@bad domain", "", false},
		{"user@<script>", "", false},
	}
	for _, tc := range cases {
		domain, ok := ParseEmailDomain(tc.in)
		if domain != tc.domain || ok != tc.ok {
			t.Errorf("ParseEmailDomain(%q) = (%q, %v), want (%q, %v)", tc.in, domain, ok, tc.domain, tc.ok)
		}
	}
}

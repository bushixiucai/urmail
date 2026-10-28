package autoconfig

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStandardAutodiscoverSchemaNegotiation(t *testing.T) {
	for _, mobile := range []bool{false, true} {
		namespace, responseSchema := outlookRequestSchema, outlookResponseSchema
		if mobile {
			namespace, responseSchema = mobileRequestSchema, mobileResponseSchema
		}
		requestXML := `<?xml version="1.0" encoding="UTF-8"?><Autodiscover xmlns="` + namespace + `"><Request><EMailAddress>user@example.com</EMailAddress><AcceptableResponseSchema>` + responseSchema + `</AcceptableResponseSchema></Request></Autodiscover>` + "\n"
		router := newRouter(t, testConfig())
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/autodiscover/autodiscover.xml", strings.NewReader(requestXML))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("standard request rejected: %d %s", w.Code, w.Body.String())
		}
		var response struct {
			Response struct {
				XMLName xml.Name
				User    struct {
					Email string `xml:"EMailAddress"`
				}
				Action struct {
					Settings struct {
						Server []struct {
							Type string
							URL  string `xml:"Url"`
						}
					}
				}
				Account struct {
					Protocols []struct {
						Type       string
						Server     string
						Port       string
						Encryption string
					} `xml:"Protocol"`
				}
			}
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Response.XMLName.Space != responseSchema {
			t.Fatalf("wrong response namespace: %s", w.Body.String())
		}
		if mobile {
			servers := response.Response.Action.Settings.Server
			if response.Response.User.Email != "user@example.com" || len(servers) != 1 || servers[0].Type != "MobileSync" || servers[0].URL != "https://mail.example.com/Microsoft-Server-ActiveSync" {
				t.Fatalf("invalid mobile response: %s", w.Body.String())
			}
		} else if len(response.Response.Account.Protocols) != 3 {
			t.Fatal("IMAP/POP3/SMTP transports not negotiated")
		}
	}
}

func TestAutodiscoverRejectsMalformedUnsupportedAndOversizedRequests(t *testing.T) {
	valid := `<Autodiscover xmlns="` + mobileRequestSchema + `"><Request><EMailAddress>user@example.com</EMailAddress><AcceptableResponseSchema>` + mobileResponseSchema + `</AcceptableResponseSchema></Request></Autodiscover>`
	for _, body := range []string{strings.Replace(valid, mobileResponseSchema, "untrusted", 1), strings.Replace(valid, "user@example.com", "", 1), valid + valid, "<!DOCTYPE a [<!ENTITY x SYSTEM 'file:///etc/passwd'>]><a>&x;</a>", strings.Repeat("x", 65<<10)} {
		router := newRouter(t, testConfig())
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/autodiscover/autodiscover.xml", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code < 400 || strings.Contains(w.Body.String(), "<Settings>") {
			t.Fatalf("invalid discovery request accepted: %d", w.Code)
		}
	}
	cfg := testConfig()
	cfg.EASPath = "-"
	router := newRouter(t, cfg)
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/autodiscover/autodiscover.xml", strings.NewReader(valid))
	req.Header.Set("Content-Type", "text/xml")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 404 || MobileAutodiscoverXML(cfg, "user@example.com") != "" {
		t.Fatal("disabled ActiveSync advertised")
	}
}

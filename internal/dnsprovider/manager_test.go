package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedProviderProtocolsUseExactZoneAndRecordIdentity(t *testing.T) {
	for _, provider := range []string{"cloudflare", "aliyun", "dnspod", "tencent"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch provider {
				case "cloudflare":
					if r.Header.Get("Authorization") != "Bearer fixture-secret" {
						t.Error("missing bearer")
					}
					switch {
					case r.URL.Path == "/zones":
						fmt.Fprint(w, `{"success":true,"result":[{"id":"123","name":"example.test"}]}`)
					case r.Method == http.MethodGet:
						if !strings.Contains(r.URL.Path, "/zones/123/") {
							t.Error("zone mismatch")
						}
						fmt.Fprint(w, `{"success":true,"result":[{"id":"456","type":"A","name":"example.test","content":"192.0.2.55","ttl":300}],"result_info":{"total_count":1}}`)
					default:
						if !strings.Contains(r.URL.Path, "/zones/123/") {
							t.Error("zone mismatch")
						}
						fmt.Fprint(w, `{"success":true,"result":{"id":"789"}}`)
					}
				case "aliyun":
					if r.URL.Query().Get("AccessKeyId") != "fixture-id" || r.URL.Query().Get("Signature") == "" {
						t.Error("missing RPC signature")
					}
					switch r.URL.Query().Get("Action") {
					case "DescribeDomainInfo":
						fmt.Fprint(w, `{"DomainId":"123","DomainName":"example.test"}`)
					case "DescribeDomainRecords":
						fmt.Fprint(w, `{"TotalCount":1,"DomainRecords":{"Record":[{"RecordId":"456","RR":"@","Type":"A","Value":"192.0.2.55","TTL":300}]}}`)
					case "AddDomainRecord", "UpdateDomainRecord", "DeleteDomainRecord":
						fmt.Fprint(w, `{"RecordId":"789"}`)
					default:
						t.Error("unexpected action")
					}
				case "dnspod":
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("login_token") != "fixture-secret" {
						t.Error("missing legacy DToken")
					}
					switch r.URL.Path {
					case "/Domain.Info":
						fmt.Fprint(w, `{"status":{"code":"1"},"domain":{"id":"123","name":"example.test"}}`)
					case "/Record.List":
						fmt.Fprint(w, `{"status":{"code":"1"},"records":[{"id":"456","name":"@","type":"A","value":"192.0.2.55","ttl":"300"}],"info":{"record_total":"1"}}`)
					default:
						fmt.Fprint(w, `{"status":{"code":"1"},"record":{"id":"789"}}`)
					}
				case "tencent":
					if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=fixture-id/") || r.Header.Get("X-TC-Version") != "2021-03-23" {
						t.Error("legacy and TC3 credentials conflated")
					}
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid json")
					}
					switch r.Header.Get("X-TC-Action") {
					case "DescribeDomainInfo":
						fmt.Fprint(w, `{"Response":{"DomainInfo":{"DomainId":123,"Domain":"example.test"}}}`)
					case "DescribeRecordList":
						fmt.Fprint(w, `{"Response":{"RecordCountInfo":{"TotalCount":1},"RecordList":[{"RecordId":456,"Type":"A","Name":"@","Value":"192.0.2.55","TTL":300}]}}`)
					default:
						fmt.Fprint(w, `{"Response":{"RecordId":789}}`)
					}
				}
			}))
			defer server.Close()
			manager, err := NewRecordManager(provider, Credentials{Token: "fixture-secret", AccessKeyID: "fixture-id", AccessKeySecret: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			manager.endpoint = server.URL
			zone, err := manager.Probe(context.Background(), "example.test")
			if err != nil || zone.ID != "123" {
				t.Fatalf("probe %+v %v", zone, err)
			}
			records, err := manager.List(context.Background(), zone)
			if err != nil || len(records) != 1 || records[0].Name != "example.test" {
				t.Fatalf("list %+v %v", records, err)
			}
			record := Record{Type: "MX", Name: "example.test", Value: "mail.example.test", Priority: 10, TTL: 300}
			created, err := manager.Create(context.Background(), zone, record)
			if err != nil || created.ID != "789" {
				t.Fatalf("create %+v %v", created, err)
			}
			if _, err = manager.Update(context.Background(), zone, created, record); err != nil {
				t.Fatal(err)
			}
			if err = manager.Delete(context.Background(), zone, created); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 5 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if _, err = manager.Create(context.Background(), zone, Record{Type: "TXT", Name: "_urmail-verify.example.test", Value: "self-proof"}); err == nil {
				t.Fatal("platform generated ownership proof")
			}
			if calls.Load() != 5 {
				t.Fatal("rejected proof reached upstream")
			}
		})
	}
}
func TestProviderErrorsAndRedirectsDoNotLeakCredential(t *testing.T) {
	var escaped atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { escaped.Add(1) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	manager, _ := NewRecordManager("cloudflare", Credentials{Token: "must-stay-private"})
	manager.endpoint = source.URL
	if _, err := manager.Probe(context.Background(), "example.test"); err == nil || strings.Contains(err.Error(), "must-stay-private") {
		t.Fatalf("unsafe error: %v", err)
	}
	if escaped.Load() != 0 {
		t.Fatal("secret-bearing redirect followed")
	}
}

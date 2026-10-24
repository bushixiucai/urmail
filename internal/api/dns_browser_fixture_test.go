package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/dnsprovider"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/onboarding"
)

// browserDNSService is a test-only, real HTTP Cloudflare contract stand-in.
// It cannot access any provider credentials or public DNS. The browser can
// inject one lost response to verify uncertain handling without real writes.
func browserDNSService(t *testing.T, pool *db.Pool, box onboarding.SecretBox) (*onboarding.Service, map[string]any) {
	t.Helper()
	var mu sync.Mutex
	records := []map[string]any{{"id": "website", "type": "A", "name": "dns-browser.example.test", "content": "192.0.2.55", "ttl": float64(300)}, {"id": "spf", "type": "TXT", "name": "dns-browser.example.test", "content": "v=spf1 include:thirdparty.example ~all", "ttl": float64(300)}}
	writes := 0
	unknown := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(map[string]any{"writes": writes, "records": records})
			return
		}
		if r.URL.Path == "/control/unknown-next" {
			unknown = true
			_ = json.NewEncoder(w).Encode(map[string]any{"armed": true})
			return
		}
		if r.Header.Get("Authorization") != "Bearer browser-dns-fixture-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/client/v4/zones" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"id": "browser-zone", "name": "example.test"}}})
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": records, "result_info": map[string]any{"total_count": len(records)}})
			return
		}
		writes++
		var payload map[string]any
		if r.Method != http.MethodDelete && json.NewDecoder(r.Body).Decode(&payload) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := fmt.Sprintf("record-%d", writes)
		if r.Method == http.MethodPost {
			payload["id"] = id
			records = append(records, payload)
		} else {
			id = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			found := false
			for i := range records {
				if records[i]["id"] == id {
					if r.Method == http.MethodDelete {
						records = append(records[:i], records[i+1:]...)
					} else {
						payload["id"] = id
						records[i] = payload
					}
					found = true
					break
				}
			}
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		if unknown {
			unknown = false
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": id}})
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := &onboarding.Service{Pool: pool, Secrets: box, Hostname: "mail.platform.test", Factory: func(provider string, credentials dnsprovider.Credentials) (dnsprovider.RecordManager, error) {
		manager, e := dnsprovider.NewRecordManager(provider, credentials)
		if e != nil {
			return nil, e
		}
		return manager.WithTransport(localDNSTransport{target: target}), nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = service.DNSOnce(ctx, "browser-dns-worker")
			}
		}
	}()
	return service, map[string]any{"zone": "example.test", "token": "browser-dns-fixture-secret", "state_url": server.URL + "/state", "unknown_url": server.URL + "/control/unknown-next", "fqdn": "dns-browser.example.test"}
}
func browserDNSSeed(t *testing.T, pool *db.Pool, tenant int64) int64 {
	t.Helper()
	key, err := domainmail.GenerateDKIMKeypair("mail")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	ctx := t.Context()
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,mode,status,verified_at,verification_token,dkim_selector,dkim_private_key,provisioning_state,custom_web_enabled) VALUES($1,'dns-browser.example.test','delegated','active',now(),'independent-fixture-proof','mail',$2,'pending',false) RETURNING id", tenant, key.PrivateKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO onboarding_tasks(tenant_id,domain_id,status,step,last_error_code) VALUES($1,$2,'blocked','dns_propagation','EXTERNAL_PROTOCOL_ACCEPTANCE_NOT_SIMULATED')", tenant, id); err != nil {
		t.Fatal(err)
	}
	return id
}

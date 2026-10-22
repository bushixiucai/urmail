package dnsprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAliyunEnsureTXTCreatesRecord(t *testing.T) {
	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("AccessKeyId") != "id" || q.Get("Signature") == "" {
			t.Errorf("missing credentials/signature: %v", q)
		}
		switch q.Get("Action") {
		case "DescribeDomainRecords":
			_, _ = w.Write([]byte(`{"DomainRecords":{"Record":[]}}`))
		case "AddDomainRecord":
			if q.Get("RR") != "_urmail-verify" {
				t.Errorf("RR = %q", q.Get("RR"))
			}
			created = true
			_, _ = w.Write([]byte(`{"RecordId":"1"}`))
		default:
			t.Errorf("unexpected action %q", q.Get("Action"))
		}
	}))
	defer srv.Close()
	a := NewAliyun("id", "secret")
	a.Base = srv.URL + "/"
	if err := a.EnsureTXT(context.Background(), "example.test", "_urmail-verify.example.test", "abc"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("record was not created")
	}
}

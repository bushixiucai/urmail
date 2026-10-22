package dnsprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDNSPodEnsureTXTCreatesRecord(t *testing.T) {
	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/Record.List":
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"records":[]}`))
		case "/Record.Create":
			if r.PostForm.Get("sub_domain") != "_urmail-verify" {
				t.Errorf("sub_domain = %q", r.PostForm.Get("sub_domain"))
			}
			if r.PostForm.Get("login_token") != "id,token" {
				t.Errorf("token = %q", r.PostForm.Get("login_token"))
			}
			created = true
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	d := NewDNSPod("id,token")
	d.Base = srv.URL
	if err := d.EnsureTXT(context.Background(), "example.test", "_urmail-verify.example.test", "abc"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("record was not created")
	}
}

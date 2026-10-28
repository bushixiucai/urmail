package backupauto

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestS3RetentionListsAndDeletesActualHTTPObjects(t *testing.T) {
	w, memory, _, _, _ := retentionWorkerFixture(t)
	_, old, _ := retentionGenerationFixture(t, memory, w.Recipient, w.now().Add(-72*time.Hour))
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=synthetic/") {
			t.Error("missing real SigV4 transport header")
		}
		calls = append(calls, request.Method+" "+request.URL.Path)
		if request.URL.Query().Get("list-type") == "2" {
			if request.URL.Query().Get("prefix") != "backups/" {
				t.Error("listing prefix not directory-scoped")
			}
			objects, e := memory.List(request.Context())
			if e != nil {
				t.Error(e)
			}
			_, _ = io.WriteString(response, "<ListBucketResult><Name>fixture-bucket</Name><IsTruncated>false</IsTruncated>")
			for _, object := range objects {
				_, _ = io.WriteString(response, "<Contents><Key>")
				_ = xml.EscapeText(response, []byte("backups/"+object.Name))
				_, _ = fmt.Fprintf(response, "</Key><Size>%d</Size></Contents>", object.Size)
			}
			_, _ = io.WriteString(response, "</ListBucketResult>")
			return
		}
		name := strings.TrimPrefix(request.URL.Path, "/fixture-bucket/backups/")
		if request.Method == http.MethodGet {
			if e := memory.Download(request.Context(), name, response); e != nil {
				t.Error(e)
			}
			return
		}
		if request.Method == http.MethodDelete {
			if e := memory.Delete(request.Context(), name); e != nil {
				t.Error(e)
			}
			response.WriteHeader(http.StatusNoContent)
			return
		}
		response.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()
	store, e := NewS3Store("fixture-bucket", "backups", S3Credentials{AccessKeyID: "synthetic", SecretKey: "synthetic-noncredential", Region: "test-1", Endpoint: server.URL})
	if e != nil {
		t.Fatal(e)
	}
	w.Store = store
	count, _, e := w.applyRetention(t.Context())
	if e != nil || count != 4 {
		t.Fatal("actual HTTP retention", count, e, calls)
	}
	for _, a := range old.Artifacts {
		if _, ok := memory.objects[a.EncryptedName]; ok {
			t.Fatal("old ciphertext remained", a.EncryptedName)
		}
	}
}
func TestS3RetentionRefusesIncompleteAndOversizedInventory(t *testing.T) {
	for _, body := range []string{
		`<ListBucketResult><Name>x</Name><IsTruncated> true </IsTruncated></ListBucketResult>`,
		`<ListBucketResult><Name>x</Name></ListBucketResult>`,
		`<ListBucketResult><Name>x</Name><IsTruncated>false</IsTruncated><NextContinuationToken>unfinished</NextContinuationToken></ListBucketResult>`,
		`<ListBucketResult><Name>x</Name><IsTruncated>false</IsTruncated><Contents><Key>foreign/urmail-20261028T110000Z.manifest.json</Key><Size>10</Size></Contents></ListBucketResult>`,
		strings.Repeat("x", (2<<20)+1),
	} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			store, e := NewS3Store("bucket", "backups", S3Credentials{AccessKeyID: "synthetic", SecretKey: "synthetic", Region: "test-1", Endpoint: server.URL})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = store.List(t.Context()); e == nil {
				t.Fatal("incomplete or unbounded listing accepted")
			}
		})
	}
}
func TestSFTPRetentionDeadlineClosesStalledSSHHandshake(t *testing.T) {
	listener, e := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	}()
	store, e := NewSFTPStore("synthetic", listener.Addr().String(), "owned", SFTPCredentials{Password: "synthetic", ConnectTimeout: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, e = store.List(ctx); e == nil || time.Since(started) > time.Second {
		t.Fatal("retention context did not stop SSH I/O", e, time.Since(started))
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owned stalled SSH connection leaked")
	}
}

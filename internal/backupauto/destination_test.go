package backupauto

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/secrets"
)

func newTestBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestDestinationValidate(t *testing.T) {
	cases := []struct {
		name        string
		destination Destination
		wantErr     bool
	}{
		{"s3 ok", Destination{Type: "s3", Bucket: "b"}, false},
		{"s3 missing bucket", Destination{Type: "s3"}, true},
		{"s3 endpoint auto-scheme", Destination{Type: "s3", Bucket: "b", Endpoint: "minio.internal:9000"}, false},
		{"s3 bad endpoint", Destination{Type: "s3", Bucket: "b", Endpoint: "ftp://x"}, true},
		{"sftp ok", Destination{Type: "sftp", Endpoint: "host:2222"}, false},
		{"sftp with scheme", Destination{Type: "sftp", Endpoint: "https://host"}, true},
		{"sftp with path", Destination{Type: "sftp", Endpoint: "host/path"}, true},
		{"webdav ok", Destination{Type: "webdav", Endpoint: "https://dav.example.com"}, false},
		{"webdav missing endpoint", Destination{Type: "webdav"}, true},
		{"webdav embedded creds", Destination{Type: "webdav", Endpoint: "https://u:p@dav.example.com"}, true},
		{"unknown type", Destination{Type: "ftp"}, true},
		{"empty type", Destination{}, true},
	}
	for _, tc := range cases {
		err := tc.destination.Validate()
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
	}
}

func TestDestinationResolveBuildsURIs(t *testing.T) {
	box := newTestBox(t)

	credsBlob, err := json.Marshal(DestinationCredentials{
		S3:       S3Credentials{AccessKeyID: "AKID", SecretKey: "SECRET", Region: "eu-west-1"},
		SFTPUser: "ops",
		WebDAV:   WebDAVCredentials{Username: "dav", Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.EncryptString(string(credsBlob))
	if err != nil {
		t.Fatal(err)
	}

	uri, creds, err := Destination{Type: "s3", Bucket: "b", Prefix: "/gen/", Endpoint: "http://minio:9000", Credentials: cipher}.Resolve(box)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "s3://b/gen" {
		t.Fatalf("s3 uri %q", uri)
	}
	if creds.S3.AccessKeyID != "AKID" || creds.S3.Endpoint != "http://minio:9000" || creds.S3.Region != "eu-west-1" {
		t.Fatalf("s3 creds %+v", creds.S3)
	}

	uri, creds, err = Destination{Type: "sftp", Endpoint: "host.example.com:2222", Prefix: "urmail", Credentials: cipher}.Resolve(box)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "sftp://ops@host.example.com:2222/urmail" {
		t.Fatalf("sftp uri %q", uri)
	}
	if creds.SFTP.Password != "" {
		t.Fatal("sftp credentials must come from the decrypted blob, not other members")
	}

	uri, creds, err = Destination{Type: "webdav", Endpoint: "https://dav.example.com:8443", Prefix: "/backups/", Credentials: cipher}.Resolve(box)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "webdavs://dav.example.com:8443/backups" {
		t.Fatalf("webdav uri %q", uri)
	}
	if creds.WebDAV.Username != "dav" || creds.WebDAV.Password != "pw" {
		t.Fatalf("webdav creds %+v", creds.WebDAV)
	}

	// http endpoint maps to the plain webdav scheme.
	uri, _, err = Destination{Type: "webdav", Endpoint: "http://dav.lan", Credentials: cipher}.Resolve(box)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "webdav://dav.lan/" {
		t.Fatalf("webdav http uri %q", uri)
	}
}

func TestDestinationResolveFailClosed(t *testing.T) {
	box := newTestBox(t)
	// Encrypted credentials without a box must fail, never fall back.
	_, _, err := Destination{Type: "s3", Bucket: "b", Credentials: "enc:v1:AAAA"}.Resolve(nil)
	if err == nil {
		t.Fatal("nil secrets box with encrypted credentials must fail")
	}
	// A corrupt ciphertext must fail, never silently pass through.
	_, _, err = Destination{Type: "s3", Bucket: "b", Credentials: "enc:v1:bm90LWF0YWxsLWEtY2lwaGVydGV4dA"}.Resolve(box)
	if err == nil {
		t.Fatal("corrupt credential ciphertext must fail")
	}
	// sftp without a username is unusable.
	plain, err := json.Marshal(DestinationCredentials{SFTP: SFTPCredentials{Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.EncryptString(string(plain))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Destination{Type: "sftp", Endpoint: "h:22", Credentials: cipher}.Resolve(box)
	if err == nil {
		t.Fatal("sftp without username must fail")
	}
}

func TestDestinationStatusOfRedactsCredentials(t *testing.T) {
	destination := Destination{
		Type: "s3", Bucket: "b", Prefix: "p", Endpoint: "http://minio:9000",
		Enabled: true, AgeRecipient: "age1public", Credentials: "enc:v1:SEEKRET",
	}
	status := destination.StatusOf()
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"SEEKRET", "enc:v1"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("status leaks %q: %s", forbidden, body)
		}
	}
	if !status.HasCredentials || !status.Enabled || status.Type != "s3" {
		t.Fatalf("status flags wrong: %+v", status)
	}
}

func TestProbeFailsClosedOnTransportFailure(t *testing.T) {
	if err := Probe(context.Background(), failingStore{}); err == nil {
		t.Fatal("probe against a broken store must fail")
	}
}

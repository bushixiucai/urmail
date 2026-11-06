package backupauto

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/webdav"
)

// newWebDAVServer starts an in-process RFC 4918 server backed by memory.
// It always runs: no container dependency.
func newWebDAVServer(t *testing.T, username, password string) *url.URL {
	t.Helper()
	handler := &webdav.Handler{
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username != "" || password != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != username || pass != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="urmail-test"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func webdavStoreFor(t *testing.T, endpoint *url.URL, prefix string, creds WebDAVCredentials) OffsiteStore {
	t.Helper()
	full := &url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host, Path: "/" + strings.TrimPrefix(prefix, "/")}
	store, err := NewWebDAVStore(full, creds)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestWebDAVStoreRoundtrip(t *testing.T) {
	endpoint := newWebDAVServer(t, "ops", "s3cret")
	store := webdavStoreFor(t, endpoint, "backups/urmail", WebDAVCredentials{Username: "ops", Password: "s3cret"})
	ctx := context.Background()

	payload := []byte("ciphertext-of-a-generation\n")
	if err := store.Upload(ctx, "urmail-20260101.dump.age", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	var got bytes.Buffer
	if err := store.Download(ctx, "urmail-20260101.dump.age", &got); err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("roundtrip content mismatch")
	}
	objects, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 1 || objects[0].Name != "urmail-20260101.dump.age" || objects[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	if err := store.Delete(ctx, "urmail-20260101.dump.age"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Download(ctx, "urmail-20260101.dump.age", &got); err == nil {
		t.Fatal("download after delete must fail")
	}
}

func TestWebDAVStoreListEmptyRoot(t *testing.T) {
	endpoint := newWebDAVServer(t, "", "")
	store := webdavStoreFor(t, endpoint, "fresh", WebDAVCredentials{})
	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list empty root: %v", err)
	}
	if len(objects) != 0 {
		t.Fatalf("expected empty listing, got %+v", objects)
	}
}

func TestWebDAVProbeRoundtrip(t *testing.T) {
	endpoint := newWebDAVServer(t, "ops", "s3cret")
	store := webdavStoreFor(t, endpoint, "backups", WebDAVCredentials{Username: "ops", Password: "s3cret"})
	if err := Probe(context.Background(), store); err != nil {
		t.Fatalf("probe: %v", err)
	}
	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("probe must clean up after itself, left %+v", objects)
	}
}

// Failure injection: wrong credentials must fail closed on every operation.
func TestWebDAVWrongCredentialsFailsClosed(t *testing.T) {
	endpoint := newWebDAVServer(t, "ops", "s3cret")
	store := webdavStoreFor(t, endpoint, "backups", WebDAVCredentials{Username: "ops", Password: "wrong"})
	ctx := context.Background()
	if err := store.Upload(ctx, "probe.age", strings.NewReader("x"), 1); err == nil {
		t.Fatal("upload with wrong password must fail")
	}
	if _, err := store.List(ctx); err == nil {
		t.Fatal("list with wrong password must fail")
	}
	if err := store.Download(ctx, "missing.age", &bytes.Buffer{}); err == nil {
		t.Fatal("download with wrong password must fail")
	}
}

func TestWebDAVStorePartFileNeverListed(t *testing.T) {
	endpoint := newWebDAVServer(t, "", "")
	store := webdavStoreFor(t, endpoint, "dav", WebDAVCredentials{})
	if err := store.Upload(context.Background(), "gen.tar.age", strings.NewReader("payload"), 7); err != nil {
		t.Fatal(err)
	}
	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if strings.HasSuffix(object.Name, ".part") {
			t.Fatalf("staged part file leaked into listing: %+v", objects)
		}
	}
}

func TestParseDestinationWebDAVSchemes(t *testing.T) {
	creds := Credentials{WebDAV: WebDAVCredentials{Username: "u", Password: "p"}}
	for scheme, want := range map[string]string{"webdav": "http", "webdavs": "https"} {
		store, err := ParseDestination(scheme+"://dav.example.com/backups", creds)
		if err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		concrete, ok := store.(*webdavStore)
		if !ok {
			t.Fatalf("%s: unexpected store type %T", scheme, store)
		}
		if concrete.scheme != want {
			t.Fatalf("%s: scheme %q, want %q", scheme, concrete.scheme, want)
		}
	}
	if _, err := ParseDestination("webdav://user:pass@dav.example.com/", creds); err == nil {
		t.Fatal("credentials embedded in the URI must be rejected: the URI persists in audit rows")
	}
	if _, err := ParseDestination("webdav://", creds); err == nil {
		t.Fatal("webdav without host must fail")
	}
	if _, err := ParseDestination("ftp://host/", creds); err == nil {
		t.Fatal("unsupported scheme must fail")
	}
}

// dockerAvailable reports whether container-based integration tests may run.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable: container roundtrip skipped")
	}
	if os.Getenv("URMAIL_SKIP_CONTAINER_TESTS") != "" {
		t.Skip("URMAIL_SKIP_CONTAINER_TESTS set")
	}
	cmd := exec.CommandContext(context.Background(), "docker", "info", "--format", "{{.ServerVersion}}")
	if err := cmd.Run(); err != nil {
		t.Skip("docker daemon unavailable: container roundtrip skipped")
	}
	return true
}

// startContainer starts a named container and returns its name. dockerOpts
// are docker flags placed before the image (env, volumes, …); containerArgs
// go after the image (image entrypoint/command arguments). A container that
// cannot start is a test-configuration error, not an environment gap, so it
// fails the test: dockerAvailable already handled environment skips.
func startContainer(t *testing.T, image string, portMappings []string, dockerOpts []string, containerArgs ...string) string {
	t.Helper()
	name := "urmail-offsite-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	full := make([]string, 0, 5+len(dockerOpts)+2*len(portMappings)+1+len(containerArgs))
	full = append(full, "run", "--rm", "-d", "--name", name)
	full = append(full, dockerOpts...)
	for _, mapping := range portMappings {
		full = append(full, "-p", "127.0.0.1:"+mapping)
	}
	full = append(full, image)
	full = append(full, containerArgs...)
	if out, err := exec.CommandContext(context.Background(), "docker", full...).CombinedOutput(); err != nil {
		t.Fatalf("cannot start %s: %v: %s", image, err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() })
	return name
}

// waitForTCPOpen polls hostPort until a connection succeeds or the deadline
// passes (container startup is asynchronous).
func waitForTCPOpen(t *testing.T, hostPort int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	dialer := &net.Dialer{Timeout: time.Second}
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(hostPort))
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("container port %d never opened", hostPort)
}

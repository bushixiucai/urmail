package backupauto

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Container roundtrips: each protocol must complete one real
// upload/list/download/delete cycle against its reference server. They run
// wherever docker exists and skip cleanly where it does not, so CI stays
// deterministic while local verification still produces real evidence.

func requireRoundtrip(t *testing.T, store OffsiteStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	payload := []byte("urmail container roundtrip ciphertext\n")
	if err := store.Upload(ctx, "roundtrip.tar.age", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	objects, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *RemoteObject
	for i := range objects {
		if objects[i].Name == "roundtrip.tar.age" {
			found = &objects[i]
		}
	}
	if found == nil {
		t.Fatalf("uploaded object missing from listing: %+v", objects)
	}
	if found.Size != int64(len(payload)) {
		t.Fatalf("listed size %d, want %d", found.Size, len(payload))
	}
	var got bytes.Buffer
	if err := store.Download(ctx, "roundtrip.tar.age", &got); err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("roundtrip content mismatch")
	}
	if err := store.Delete(ctx, "roundtrip.tar.age"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	objects, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Name == "roundtrip.tar.age" {
			t.Fatal("delete did not remove the object")
		}
	}
	if err := Probe(ctx, store); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

// minioCreateBucket provisions the roundtrip bucket via a signed PUT; the
// s3 store never creates buckets so the test must. The PUT is retried
// because S3-compatible servers may accept TCP connections before their
// storage backend is fully initialized (the connection then EOFs).
func minioCreateBucket(t *testing.T, hostBase, bucket string) {
	t.Helper()
	target := "http://" + hostBase + "/" + bucket + "/"
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		headers := signV4(http.MethodPut, hostBase, "/"+bucket+"/", "", "us-east-1", "s3", "minioadmin", "minioadmin", emptyPayloadHash, time.Now().UTC())
		for key, values := range headers {
			for _, value := range values {
				req.Header.Set(key, value)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusConflict {
				return
			}
			err = fmt.Errorf("create bucket: unexpected status %d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// s3TestImage is the S3-compatible server image for the container roundtrip.
// Default is rustfs because MinIO's community images are no longer published
// to Docker Hub and anonymous pulls are denied in CI and several networks;
// URMAIL_S3_TEST_IMAGE overrides it. Any S3-compatible server works because
// the test exercises the wire protocol, not server specifics.
func s3TestImage() string {
	if image := os.Getenv("URMAIL_S3_TEST_IMAGE"); image != "" {
		return image
	}
	return "rustfs/rustfs:latest"
}

func TestS3ContainerRoundtrip(t *testing.T) {
	dockerAvailable(t)
	const hostPort = 39011
	hostBase := "127.0.0.1:" + strconv.Itoa(hostPort)
	image := s3TestImage()
	// MINIO_ROOT_USER/PASSWORD is honored by MinIO natively and by rustfs
	// (which also accepts it for MinIO compatibility), pinning credentials
	// the test signs with instead of relying on image-specific defaults.
	startContainer(t, image, []string{strconv.Itoa(hostPort) + ":9000"},
		[]string{"-e", "MINIO_ROOT_USER=minioadmin", "-e", "MINIO_ROOT_PASSWORD=minioadmin"},
		"server", "/data")
	waitForTCPOpen(t, hostPort)
	minioCreateBucket(t, hostBase, "urmail-roundtrip")
	store, err := ParseDestination("s3://urmail-roundtrip/backups", Credentials{S3: S3Credentials{
		AccessKeyID: "minioadmin", SecretKey: "minioadmin",
		Endpoint: "http://" + hostBase,
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireRoundtrip(t, store)
}

// sftpHostFingerprint performs one SSH handshake without pinning and
// returns the server's SHA256 fingerprint, mirroring how an operator
// collects it before configuring production. The handshake is retried:
// sshd may accept TCP before the host keys are fully written, closing
// early connections with an EOF.
func sftpHostFingerprint(t *testing.T, hostPort string) string {
	t.Helper()
	config := &ssh.ClientConfig{
		User:            "ops",
		Auth:            []ssh.AuthMethod{ssh.Password("s3cret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // fingerprint is recorded below instead
		Timeout:         10 * time.Second,
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		var fingerprint string
		config.HostKeyCallback = func(_ string, _ net.Addr, observed ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(observed)
			return nil
		}
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(context.Background(), "tcp", hostPort)
		if err == nil {
			serverConn, _, _, hsErr := ssh.NewClientConn(conn, hostPort, config)
			if hsErr == nil {
				_ = serverConn.Close()
				if fingerprint != "" {
					return fingerprint
				}
				hsErr = fmt.Errorf("handshake completed without observing the host key")
			}
			_ = conn.Close()
			err = hsErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("ssh handshake never succeeded: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestSFTPContainerRoundtrip(t *testing.T) {
	dockerAvailable(t)
	const hostPort = 39012
	hostPortStr := "127.0.0.1:" + strconv.Itoa(hostPort)
	// The chroot root of atmoz/sftp is root-owned, so the store's best-effort
	// MkdirAll cannot create /upload; the destination directory must exist,
	// mirroring a real operator provisioning it. Mount one in.
	uploadDir := t.TempDir()
	volume := "-v" + uploadDir + ":/home/ops/upload"
	startContainer(t, "atmoz/sftp:alpine", []string{strconv.Itoa(hostPort) + ":22"},
		[]string{volume}, "ops:s3cret:1001")
	waitForTCPOpen(t, hostPort)
	fingerprint := sftpHostFingerprint(t, hostPortStr)
	creds := Credentials{SFTP: SFTPCredentials{Password: "s3cret", HostKeyFingerprint: fingerprint}}
	uri := "sftp://ops@" + hostPortStr + "/upload"
	pinned, err := ParseDestination(uri, creds)
	if err != nil {
		t.Fatal(err)
	}
	requireRoundtrip(t, pinned)
	// Failure injection: a mismatched pinned fingerprint must fail closed.
	creds.SFTP.HostKeyFingerprint = "sha256-not-the-real-key"
	wrong, err := ParseDestination(uri, creds)
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Upload(context.Background(), "x.age", strings.NewReader("x"), 1); err == nil {
		t.Fatal("upload against a mismatched host key must fail")
	}
}

func TestWebDAVContainerRoundtrip(t *testing.T) {
	dockerAvailable(t)
	const hostPort = 39013
	hostPortStr := strconv.Itoa(hostPort)
	startContainer(t, "bytemark/webdav:2.4", []string{hostPortStr + ":80"},
		[]string{"-e", "AUTH_TYPE=Basic", "-e", "USERNAME=ops", "-e", "PASSWORD=s3cret"})
	waitForTCPOpen(t, hostPort)
	time.Sleep(2 * time.Second)
	store, err := ParseDestination("webdav://127.0.0.1:"+hostPortStr+"/", Credentials{WebDAV: WebDAVCredentials{Username: "ops", Password: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRoundtrip(t, store)
}

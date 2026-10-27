package backupauto

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPCredentials authenticate one generic SFTP endpoint.
type SFTPCredentials struct {
	// Password is used when PrivateKeyPath is empty.
	Password string
	// PrivateKeyPath is a PEM private key readable by the daemon.
	PrivateKeyPath string
	// HostKeyFingerprint, when set, pins the expected SHA256 host key
	// fingerprint (base64, without padding). Empty accepts any host key and
	// is only acceptable for development.
	HostKeyFingerprint string
	// ConnectTimeout bounds the TCP+SSH handshake. Zero defaults to 30s.
	ConnectTimeout time.Duration
}

// sftpStore implements OffsiteStore over a generic SSH File Transfer
// (SFTP) server using github.com/pkg/sftp on top of golang.org/x/crypto/ssh,
// which is already an indirect dependency of this module.
type sftpStore struct {
	user     string
	hostPort string
	root     string
	creds    SFTPCredentials
}

// NewSFTPStore creates a generic SFTP offsite store rooted at root.
func NewSFTPStore(user, hostPort, root string, creds SFTPCredentials) (OffsiteStore, error) {
	if user == "" || hostPort == "" {
		return nil, fmt.Errorf("sftp destination requires user and host")
	}
	if creds.ConnectTimeout <= 0 {
		creds.ConnectTimeout = 30 * time.Second
	}
	return &sftpStore{user: user, hostPort: hostPort, root: strings.Trim(root, "/"), creds: creds}, nil
}

func (s *sftpStore) connect(ctx context.Context) (*sftp.Client, func(), error) {
	timeout := s.creds.ConnectTimeout
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", s.hostPort)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp dial %s: %w", s.hostPort, err)
	}
	config := &ssh.ClientConfig{
		User:            s.user,
		Auth:            s.authMethods(),
		HostKeyCallback: s.hostKeyCallback(),
		Timeout:         timeout,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, s.hostPort, config)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sftp handshake %s: %w", s.hostPort, err)
	}
	client, err := sftp.NewClient(ssh.NewClient(sshConn, chans, reqs))
	if err != nil {
		_ = sshConn.Close()
		return nil, nil, fmt.Errorf("sftp subsystem: %w", err)
	}
	done := false
	cleanup := func() {
		if done {
			return
		}
		done = true
		_ = client.Close()
	}
	return client, cleanup, nil
}

func (s *sftpStore) authMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	if s.creds.PrivateKeyPath != "" {
		if key, err := os.ReadFile(s.creds.PrivateKeyPath); err == nil { // #nosec G304 -- operator-configured key path.
			if signer, err := ssh.ParsePrivateKey(key); err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}
	if s.creds.Password != "" {
		methods = append(methods, ssh.Password(s.creds.Password))
	}
	return methods
}

func (s *sftpStore) hostKeyCallback() ssh.HostKeyCallback {
	fingerprint := s.creds.HostKeyFingerprint
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if fingerprint == "" {
			return nil // development only; production sets a pinned fingerprint
		}
		actual := ssh.FingerprintSHA256(key)
		if actual != fingerprint {
			return fmt.Errorf("sftp host key fingerprint mismatch: got %s want %s", actual, fingerprint)
		}
		return nil
	}
}

func (s *sftpStore) remotePath(name string) string {
	return path.Join("/", s.root, name)
}

func (s *sftpStore) Upload(ctx context.Context, name string, r io.Reader, _ int64) error {
	client, cleanup, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	target := s.remotePath(name)
	if dir := path.Dir(target); dir != "/" {
		_ = client.MkdirAll(dir)
	}
	tmp := target + ".part"
	f, err := client.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = client.Remove(tmp)
		return fmt.Errorf("sftp upload %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = client.Remove(tmp)
		return fmt.Errorf("sftp close %s: %w", tmp, err)
	}
	// Atomic rename so a crashed upload never leaves a half-written object
	// that a later retention pass could mistake for a complete generation.
	if err := client.PosixRename(tmp, target); err != nil {
		_ = client.Remove(tmp)
		return fmt.Errorf("sftp rename %s: %w", target, err)
	}
	return nil
}

func (s *sftpStore) List(ctx context.Context) ([]RemoteObject, error) {
	client, cleanup, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	entries, err := client.ReadDir(path.Join("/", s.root))
	if err != nil {
		return nil, fmt.Errorf("sftp readdir %s: %w", s.root, err)
	}
	objects := make([]RemoteObject, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		objects = append(objects, RemoteObject{Name: e.Name(), Size: e.Size(), LastModified: e.ModTime()})
	}
	return objects, nil
}

func (s *sftpStore) Download(ctx context.Context, name string, w io.Writer) error {
	client, cleanup, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	f, err := client.Open(s.remotePath(name))
	if err != nil {
		return fmt.Errorf("sftp open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

func (s *sftpStore) Delete(ctx context.Context, name string) error {
	client, cleanup, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	return client.Remove(s.remotePath(name))
}

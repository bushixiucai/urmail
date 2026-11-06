package backupauto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// DestinationSettingsKey is the settings-table key (scope 'platform',
// scope_id 0) holding the platform-admin-configured optional offsite
// backup destination. Non-secret fields persist as plaintext JSON; the
// Credentials field is an enc:v1: ciphertext produced by internal/secrets.
const DestinationSettingsKey = "backup_offsite_destination"

// Destination is the persisted offsite destination configuration.
type Destination struct {
	// Type selects the adapter: "s3" (incl. S3-compatible), "sftp" or "webdav".
	Type string `json:"type"`
	// Endpoint is the non-secret address: for s3 an optional custom endpoint
	// (empty = AWS), for sftp "host[:port]", for webdav "http(s)://host[:port]".
	Endpoint string `json:"endpoint,omitempty"`
	// Bucket is the s3 bucket (s3 only).
	Bucket string `json:"bucket,omitempty"`
	// Prefix is the optional remote directory/object prefix.
	Prefix string `json:"prefix,omitempty"`
	// Enabled turns the destination on. An unconfigured or disabled
	// destination means automatic offsite upload is disabled.
	Enabled bool `json:"enabled"`
	// AgeRecipient optionally carries the age public key so the automatic
	// backup worker can run from this row when no environment variable is
	// set. It is a public key, never a secret.
	AgeRecipient string `json:"age_recipient,omitempty"`
	// Credentials is enc:v1: ciphertext of DestinationCredentials JSON.
	// It is never returned by any API and never logged or audited.
	Credentials string `json:"credentials,omitempty"`
}

// DestinationCredentials is the plaintext JSON encrypted into
// Destination.Credentials. Only the members matching Type carry values.
type DestinationCredentials struct {
	S3       S3Credentials     `json:"s3,omitempty"`
	SFTP     SFTPCredentials   `json:"sftp,omitempty"`
	SFTPUser string            `json:"sftp_user,omitempty"`
	WebDAV   WebDAVCredentials `json:"webdav,omitempty"`
}

// Validate enforces the per-type shape before persistence or use. Fail-closed:
// anything unexpected is rejected rather than guessed at upload time.
func (d Destination) Validate() error {
	switch d.Type {
	case "s3":
		if d.Bucket == "" {
			return fmt.Errorf("s3 destination requires bucket")
		}
		if d.Endpoint != "" {
			if !strings.Contains(d.Endpoint, "://") {
				d.Endpoint = "https://" + d.Endpoint
			}
			parsed, err := url.Parse(d.Endpoint)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("s3 endpoint must be an http(s) URL")
			}
		}
	case "sftp":
		if d.Endpoint == "" || strings.Contains(d.Endpoint, "://") || strings.Contains(d.Endpoint, "/") {
			return fmt.Errorf("sftp endpoint must be host[:port] without a scheme or path")
		}
	case "webdav":
		parsed, err := url.Parse(d.Endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("webdav endpoint must be an http(s) URL")
		}
		if parsed.User != nil {
			return fmt.Errorf("webdav endpoint must not embed credentials")
		}
	default:
		return fmt.Errorf("destination type must be s3, sftp or webdav")
	}
	return nil
}

// SecretBox is the subset of internal/secrets used to seal and open
// destination credentials; *secrets.Box satisfies it.
type SecretBox interface {
	EncryptString(plain string) (string, error)
	DecryptString(value string) (string, error)
}

// Resolve decrypts the stored credentials and derives the offsite URI plus
// transport credentials, mirroring the environment-variable contract so both
// configuration paths converge on ParseDestination.
func (d Destination) Resolve(box SecretBox) (string, Credentials, error) {
	if err := d.Validate(); err != nil {
		return "", Credentials{}, err
	}
	var dc DestinationCredentials
	if d.Credentials != "" {
		if box == nil {
			return "", Credentials{}, fmt.Errorf("offsite destination credentials require a configured secrets box")
		}
		plain, err := box.DecryptString(d.Credentials)
		if err != nil {
			return "", Credentials{}, fmt.Errorf("decrypt offsite destination credentials: %w", err)
		}
		if err := json.Unmarshal([]byte(plain), &dc); err != nil {
			return "", Credentials{}, fmt.Errorf("decode offsite destination credentials: %w", err)
		}
	}
	var creds Credentials
	uri := ""
	switch d.Type {
	case "s3":
		creds.S3 = dc.S3
		creds.S3.Endpoint = d.Endpoint
		uri = "s3://" + d.Bucket
		if d.Prefix != "" {
			uri += "/" + strings.Trim(d.Prefix, "/")
		}
	case "sftp":
		if dc.SFTPUser == "" {
			return "", Credentials{}, fmt.Errorf("sftp destination requires a username")
		}
		creds.SFTP = dc.SFTP
		uri = "sftp://" + dc.SFTPUser + "@" + d.Endpoint
		if d.Prefix != "" {
			uri += "/" + strings.Trim(d.Prefix, "/")
		}
	case "webdav":
		creds.WebDAV = dc.WebDAV
		parsed, _ := url.Parse(d.Endpoint)
		scheme := "webdav"
		if parsed.Scheme == "https" {
			scheme = "webdavs"
		}
		uri = scheme + "://" + parsed.Host + path.Join("/", parsed.Path, strings.Trim(d.Prefix, "/"))
	}
	return uri, creds, nil
}

// Store builds the OffsiteStore for this destination using the same
// ParseDestination contract as the CLI, so test, backup and restore all
// run one verified transport implementation per protocol.
func (d Destination) Store(box SecretBox) (OffsiteStore, string, error) {
	uri, creds, err := d.Resolve(box)
	if err != nil {
		return nil, "", err
	}
	store, err := ParseDestination(uri, creds)
	if err != nil {
		return nil, "", err
	}
	return store, uri, nil
}

// Status is the credential-free destination view for the admin API.
type Status struct {
	Configured     bool   `json:"configured"`
	Enabled        bool   `json:"enabled"`
	Type           string `json:"type,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Bucket         string `json:"bucket,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	AgeRecipient   string `json:"age_recipient,omitempty"`
	HasCredentials bool   `json:"has_credentials"`
	// CredentialSummary reports which credential kinds are present without
	// revealing any value.
	CredentialSummary map[string]bool `json:"credential_summary,omitempty"`
}

// StatusOf redacts the destination into its API view.
func (d Destination) StatusOf() Status {
	status := Status{
		Configured:     d.Type != "",
		Enabled:        d.Enabled,
		Type:           d.Type,
		Endpoint:       d.Endpoint,
		Bucket:         d.Bucket,
		Prefix:         d.Prefix,
		AgeRecipient:   d.AgeRecipient,
		HasCredentials: d.Credentials != "",
	}
	return status
}

// Probe exercises one full upload/download/delete roundtrip against the
// store so administrators verify connectivity and write permission before
// the first scheduled backup depends on them.
func Probe(ctx context.Context, store OffsiteStore) error {
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return fmt.Errorf("probe seed: %w", err)
	}
	name := "urmail-probe-" + hex.EncodeToString(seed[:]) + ".txt"
	payload := []byte("urmail offsite probe " + hex.EncodeToString(seed[:]) + "\n")
	if err := store.Upload(ctx, name, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		return fmt.Errorf("probe upload: %w", err)
	}
	var got strings.Builder
	if err := store.Download(ctx, name, &got); err != nil {
		_ = store.Delete(ctx, name)
		return fmt.Errorf("probe download: %w", err)
	}
	if got.String() != string(payload) {
		_ = store.Delete(ctx, name)
		return fmt.Errorf("probe download: roundtrip content mismatch")
	}
	if err := store.Delete(ctx, name); err != nil {
		return fmt.Errorf("probe delete: %w", err)
	}
	return nil
}

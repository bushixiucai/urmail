package backupauto

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// RemoteObject is one object observed at the offsite destination.
type RemoteObject struct {
	Name         string
	Size         int64
	LastModified time.Time
}

// OffsiteStore is the transport contract for offsite destinations. Both
// implementations stream; no full artifact is ever held in memory.
type OffsiteStore interface {
	Upload(ctx context.Context, name string, r io.Reader, size int64) error
	List(ctx context.Context) ([]RemoteObject, error)
	Download(ctx context.Context, name string, w io.Writer) error
	// Delete removes name. Callers must only pass names approved by the
	// retention planner.
	Delete(ctx context.Context, name string) error
}

// ParseDestination builds the matching store from an offsite URI:
// s3://bucket/prefix (AWS or S3-compatible) or sftp://user@host[:port]/path.
func ParseDestination(uri string, creds Credentials) (OffsiteStore, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse offsite URI: %w", err)
	}
	switch parsed.Scheme {
	case "s3":
		return NewS3Store(parsed.Host, strings.TrimPrefix(parsed.Path, "/"), creds.S3)
	case "sftp":
		host := parsed.Host
		if parsed.Port() == "" {
			host += ":22"
		}
		return NewSFTPStore(parsed.User.Username(), host, strings.TrimPrefix(parsed.Path, "/"), creds.SFTP)
	default:
		return nil, fmt.Errorf("unsupported offsite scheme %q (want s3:// or sftp://)", parsed.Scheme)
	}
}

// Credentials bundles per-scheme credentials; unused members stay empty.
type Credentials struct {
	S3   S3Credentials
	SFTP SFTPCredentials
}

// S3Credentials authenticate one S3-compatible endpoint.
type S3Credentials struct {
	AccessKeyID string
	SecretKey   string
	Region      string
	// Endpoint overrides the AWS virtual-hosted URL for S3-compatible
	// object stores (MinIO, Ceph, provider object storage). Empty means AWS.
	Endpoint string
}

// s3Store is a minimal S3-compatible client signed with SigV4, implemented
// directly on net/http (no AWS SDK): the backup path needs exactly four
// object operations and the dependency surface must stay small.
type s3Store struct {
	client    *http.Client
	bucket    string
	prefix    string
	creds     S3Credentials
	scheme    string
	hostBase  string
	pathStyle bool
}

// NewS3Store creates an S3-compatible offsite store.
func NewS3Store(bucket, prefix string, creds S3Credentials) (OffsiteStore, error) {
	if bucket == "" || creds.AccessKeyID == "" || creds.SecretKey == "" {
		return nil, fmt.Errorf("s3 destination requires bucket and access keys")
	}
	if creds.Region == "" {
		creds.Region = "us-east-1"
	}
	scheme, hostBase, pathStyle := "https", bucket+".s3."+creds.Region+".amazonaws.com", false
	if creds.Endpoint != "" {
		endpoint := creds.Endpoint
		if !strings.Contains(endpoint, "://") {
			endpoint = "https://" + endpoint
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse s3 endpoint: %w", err)
		}
		scheme, hostBase, pathStyle = parsed.Scheme, parsed.Host, true
	}
	return &s3Store{client: http.DefaultClient, bucket: bucket, prefix: prefix, creds: creds, scheme: scheme, hostBase: hostBase, pathStyle: pathStyle}, nil
}

// requestTarget returns the request path element and the Host header value
// for one object. AWS uses virtual-host style (bucket in the hostname);
// custom endpoints use path style because bucket subdomains rarely resolve.
func (s *s3Store) requestTarget(name string) (pathElem, host string) {
	if s.pathStyle {
		return "/" + s.bucket + "/" + s.prefix + "/" + name, s.hostBase
	}
	return "/" + s.prefix + "/" + name, s.hostBase
}

// signV4 computes the AWS Signature Version 4 headers. The canonical
// (lowercase) header names used FOR SIGNING are independent of the
// canonical Go http.Header keys returned here. Separated from the request
// round-trip so the canonical form is testable.
func signV4(method, host, path, query, region, service, accessKey, secretKey, payloadHash string, t time.Time) http.Header {
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")
	canonicalHeaders := "host:" + host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{method, path, sortQuery(query), canonicalHeaders, signedHeaders, payloadHash}, "\n")
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))
	return http.Header{
		"Authorization":        {"AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature},
		"X-Amz-Date":           {amzDate},
		"X-Amz-Content-Sha256": {payloadHash},
	}
}

func sortQuery(query string) string {
	if query == "" {
		return ""
	}
	pairs := strings.Split(query, "&")
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *s3Store) do(ctx context.Context, method, name string, body io.Reader, length int64, query string) (*http.Response, error) {
	pathElem, host := s.requestTarget(name)
	querySuffix := ""
	if query != "" {
		querySuffix = "?" + query
	}
	pathForURL := (&url.URL{Path: pathElem}).EscapedPath()
	payloadHash := emptyPayloadHash
	if body != nil {
		// Streaming uploads are signed with the UNSIGNED-PAYLOAD marker,
		// which every S3-compatible implementation accepts over TLS.
		payloadHash = "UNSIGNED-PAYLOAD"
	}
	header := signV4(method, host, pathForURL, query, s.creds.Region, "s3", s.creds.AccessKeyID, s.creds.SecretKey, payloadHash, time.Now())
	req, err := http.NewRequestWithContext(ctx, method, s.scheme+"://"+s.hostBase+pathForURL+querySuffix, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if length >= 0 {
		req.ContentLength = length
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("s3 %s %s: HTTP %d: %s", method, name, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

func (s *s3Store) Upload(ctx context.Context, name string, r io.Reader, size int64) error {
	resp, err := s.do(ctx, http.MethodPut, name, r, size, "")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (s *s3Store) List(ctx context.Context) ([]RemoteObject, error) {
	query := "list-type=2"
	if s.prefix != "" {
		query = "list-type=2&prefix=" + url.QueryEscape(s.prefix)
	}
	resp, err := s.do(ctx, http.MethodGet, "", nil, -1, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	objects, err := parseS3ListXML(string(body), s.prefix)
	if err != nil {
		return nil, err
	}
	// A truncated listing must never look like an empty destination, or a
	// later retention pass could believe older generations no longer exist.
	if strings.Contains(string(body), "<IsTruncated>true</IsTruncated>") {
		return nil, fmt.Errorf("s3 listing is truncated; refusing to plan retention from an incomplete view")
	}
	return objects, nil
}

func (s *s3Store) Download(ctx context.Context, name string, w io.Writer) error {
	resp, err := s.do(ctx, http.MethodGet, name, nil, -1, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (s *s3Store) Delete(ctx context.Context, name string) error {
	resp, err := s.do(ctx, http.MethodDelete, name, nil, -1, "")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

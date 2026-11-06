package backupauto

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// WebDAVCredentials authenticate one generic WebDAV endpoint. Username may
// stay empty for anonymous development servers; production always sets both.
type WebDAVCredentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// webdavStore implements OffsiteStore over a generic WebDAV (RFC 4918)
// server using only net/http: PUT upload, GET download, DELETE removal and
// a Depth-1 PROPFIND listing. Uploads stage a ".part" object and MOVE it
// into place so a crashed upload never leaves a half-written object a
// later retention pass could mistake for a complete generation.
type webdavStore struct {
	client *http.Client
	scheme string // http for webdav://, https for webdavs://
	host   string
	root   string
	creds  WebDAVCredentials
}

// NewWebDAVStore creates a WebDAV offsite store from a parsed destination
// URI (scheme webdav or webdavs). The URI must not carry user information:
// credentials belong in the encrypted credential store, never in the URI
// that backup_runs and audit records persist.
func NewWebDAVStore(uri *url.URL, creds WebDAVCredentials) (OffsiteStore, error) {
	if uri.User != nil {
		return nil, fmt.Errorf("webdav URI must not embed credentials; set them in the encrypted destination credentials")
	}
	if uri.Host == "" {
		return nil, fmt.Errorf("webdav destination requires a host")
	}
	scheme := uri.Scheme
	switch scheme {
	case "webdav", "http":
		scheme = "http"
	default: // webdavs or https
		scheme = "https"
	}
	return &webdavStore{client: http.DefaultClient, scheme: scheme, host: uri.Host, root: strings.Trim(uri.Path, "/"), creds: creds}, nil
}

// objectURL builds the request URL for one object below the configured root.
func (s *webdavStore) objectURL(name string) *url.URL {
	return &url.URL{Scheme: s.scheme, Host: s.host, Path: path.Join("/", s.root, name)}
}

func (s *webdavStore) authorize(req *http.Request) {
	if s.creds.Username != "" || s.creds.Password != "" {
		req.SetBasicAuth(s.creds.Username, s.creds.Password)
	}
}

// mkcolRoot creates the configured root directory, tolerating servers that
// report it as existing (405/301) and failing closed on anything else.
func (s *webdavStore) mkcolRoot(ctx context.Context) error {
	trimmed := strings.Trim(s.root, "/")
	if trimmed == "" {
		return nil
	}
	segments := strings.Split(trimmed, "/")
	for i := range segments {
		target := &url.URL{Scheme: s.scheme, Host: s.host, Path: "/" + path.Join("/", strings.Join(segments[:i+1], "/"))}
		req, err := http.NewRequestWithContext(ctx, "MKCOL", target.String(), nil)
		if err != nil {
			return fmt.Errorf("webdav mkcol request: %w", err)
		}
		s.authorize(req)
		resp, err := s.client.Do(req)
		if err != nil {
			return fmt.Errorf("webdav mkcol %s: %w", target.Path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusCreated {
			continue
		}
		return fmt.Errorf("webdav mkcol %s: unexpected status %d", target.Path, resp.StatusCode)
	}
	return nil
}

func (s *webdavStore) Upload(ctx context.Context, name string, r io.Reader, _ int64) error {
	if err := s.mkcolRoot(ctx); err != nil {
		return err
	}
	part := s.objectURL(name + ".part")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.String(), r)
	if err != nil {
		return fmt.Errorf("webdav put request: %w", err)
	}
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav put %s: %w", name, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webdav put %s: unexpected status %d", name, resp.StatusCode)
	}
	final := s.objectURL(name)
	move, err := http.NewRequestWithContext(ctx, "MOVE", part.String(), nil)
	if err != nil {
		return fmt.Errorf("webdav move request: %w", err)
	}
	move.Header.Set("Destination", final.String())
	move.Header.Set("Overwrite", "T")
	s.authorize(move)
	moveResp, err := s.client.Do(move)
	if err != nil {
		return fmt.Errorf("webdav move %s: %w", name, err)
	}
	_, _ = io.Copy(io.Discard, moveResp.Body)
	_ = moveResp.Body.Close()
	if moveResp.StatusCode < 200 || moveResp.StatusCode > 299 {
		_ = s.deleteObject(ctx, part)
		return fmt.Errorf("webdav move %s: unexpected status %d", name, moveResp.StatusCode)
	}
	return nil
}

// davListXML is the subset of PROPFIND multistatus output the planner needs.
type davListXML struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string      `xml:"href"`
	Propstat []davPropst `xml:"propstat"`
}

type davPropst struct {
	Status string   `xml:"status"`
	Prop   davProps `xml:"prop"`
}

type davProps struct {
	// Collection is present only for directory entries.
	Collection []xml.Name `xml:"resourcetype>collection"`
	// ContentLength stays 0 for entries without a numeric property value.
	ContentLength int64  `xml:"getcontentlength"`
	LastModified  string `xml:"getlastmodified"`
}

// parseWebDAVListXML turns a Depth-1 multistatus body into remote objects,
// refusing truncated or oversized inventories like the s3 listing does.
func parseWebDAVListXML(body, rootPath string) ([]RemoteObject, error) {
	var parsed davListXML
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("decode webdav listing: %w", err)
	}
	if len(parsed.Responses) == 0 {
		return nil, fmt.Errorf("decode webdav listing: empty multistatus")
	}
	if len(parsed.Responses)-1 > retentionMaxObjects {
		return nil, fmt.Errorf("webdav listing object budget exceeded")
	}
	objects := make([]RemoteObject, 0, len(parsed.Responses))
	for _, entry := range parsed.Responses {
		href := entry.Href
		if parsed, err := url.Parse(href); err == nil && parsed.Path != "" {
			href = parsed.Path
		}
		if unescaped, err := url.PathUnescape(href); err == nil {
			href = unescaped
		}
		if path.Clean("/"+href) == path.Clean("/"+rootPath) {
			continue // the root collection itself
		}
		isDir := false
		var props davProps
		for _, stat := range entry.Propstat {
			if len(stat.Prop.Collection) > 0 {
				isDir = true
				break
			}
			if props.ContentLength == 0 {
				props = stat.Prop
			}
		}
		if isDir {
			continue
		}
		name := path.Base(path.Clean("/" + href))
		if name == "" || name == "/" || name == "." {
			continue
		}
		var lastModified time.Time
		if props.LastModified != "" {
			if parsed, err := http.ParseTime(props.LastModified); err == nil {
				lastModified = parsed
			}
		}
		objects = append(objects, RemoteObject{Name: name, Size: props.ContentLength, LastModified: lastModified})
	}
	return objects, nil
}

func (s *webdavStore) List(ctx context.Context) ([]RemoteObject, error) {
	root := s.objectURL("")
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", root.String(), strings.NewReader(`<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop></d:propfind>`))
	if err != nil {
		return nil, fmt.Errorf("webdav propfind request: %w", err)
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", `application/xml; charset="utf-8"`)
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav propfind: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, propfindMaxBytes))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// An empty destination is a valid listing for a fresh store.
		if readErr == nil {
			return []RemoteObject{}, nil
		}
		return nil, fmt.Errorf("webdav propfind: read failed: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("webdav propfind: unexpected status %d", resp.StatusCode)
	}
	if readErr != nil {
		return nil, fmt.Errorf("webdav propfind: read failed: %w", readErr)
	}
	return parseWebDAVListXML(string(body), root.Path)
}

// propfindMaxBytes bounds a Depth-1 listing; real inventories stay far below.
const propfindMaxBytes = 8 << 20

func (s *webdavStore) Download(ctx context.Context, name string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(name).String(), nil)
	if err != nil {
		return fmt.Errorf("webdav get request: %w", err)
	}
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav get %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("webdav object %s missing", name)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webdav get %s: unexpected status %d", name, resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (s *webdavStore) deleteObject(ctx context.Context, target *url.URL) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target.String(), nil)
	if err != nil {
		return err
	}
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}

func (s *webdavStore) Delete(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objectURL(name).String(), nil)
	if err != nil {
		return fmt.Errorf("webdav delete request: %w", err)
	}
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav delete %s: %w", name, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone: deletion stays idempotent
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webdav delete %s: unexpected status %d", name, resp.StatusCode)
	}
	return nil
}

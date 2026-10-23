package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider can write the DNS records needed for domain onboarding.
type Provider interface {
	EnsureTXT(ctx context.Context, zone, name, value string) error
}

// Cloudflare is a minimal Cloudflare v4 DNS client. It only supports the TXT
// records used for domain verification and DKIM; it deliberately does not
// expose arbitrary record management.
type Cloudflare struct {
	Token  string
	Client *http.Client
	Base   string
}

// NewCloudflare creates a Cloudflare DNS client.
func NewCloudflare(token string) *Cloudflare {
	return &Cloudflare{
		Token:  token,
		Client: &http.Client{Timeout: 15 * time.Second},
		Base:   "https://api.cloudflare.com/client/v4",
	}
}

type cfResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (c *Cloudflare) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.Success {
		msg := "cloudflare request failed"
		if len(out.Errors) > 0 {
			msg = out.Errors[0].Message
		}
		return nil, fmt.Errorf("%s (status %d)", msg, resp.StatusCode)
	}
	return out.Result, nil
}

func (c *Cloudflare) zoneID(ctx context.Context, zone string) (string, error) {
	result, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("cloudflare zone %q not found", zone)
	}
	return zones[0].ID, nil
}

// EnsureTXT creates or updates a TXT record in the given zone.
func (c *Cloudflare) EnsureTXT(ctx context.Context, zone, name, value string) error {
	id, err := c.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	result, err := c.do(ctx, http.MethodGet,
		"/zones/"+url.PathEscape(id)+"/dns_records?type=TXT&name="+url.QueryEscape(name), nil)
	if err != nil {
		return err
	}
	var records []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &records); err != nil {
		return err
	}
	payload := map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 120}
	if len(records) > 0 {
		_, err = c.do(ctx, http.MethodPut,
			"/zones/"+url.PathEscape(id)+"/dns_records/"+url.PathEscape(records[0].ID), payload)
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(id)+"/dns_records", payload)
	return err
}

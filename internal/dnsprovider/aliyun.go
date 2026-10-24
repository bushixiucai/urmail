// Package dnsprovider implements supported DNS provider API clients.
package dnsprovider

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Alibaba Cloud DNS signature version 1 requires HMAC-SHA1.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Aliyun is the Alibaba Cloud DNS TXT client.
type Aliyun struct {
	AccessKeyID     string
	AccessKeySecret string
	Client          *http.Client
	Base            string
}

// NewAliyun creates the Alibaba Cloud DNS client.
func NewAliyun(id, secret string) *Aliyun {
	return &Aliyun{
		AccessKeyID: id, AccessKeySecret: secret,
		Client: &http.Client{Timeout: 15 * time.Second},
		Base:   "https://alidns.aliyuncs.com/",
	}
}

func percentEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}

func (a *Aliyun) call(ctx context.Context, action string, params url.Values) ([]byte, error) {
	params.Set("Action", action)
	params.Set("Format", "JSON")
	params.Set("Version", "2015-01-09")
	params.Set("AccessKeyId", a.AccessKeyID)
	params.Set("SignatureMethod", "HMAC-SHA1")
	params.Set("SignatureVersion", "1.0")
	params.Set("SignatureNonce", fmt.Sprintf("%d", time.Now().UnixNano()))
	params.Set("Timestamp", time.Now().UTC().Format("2006-01-02T15:04:05Z"))

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		if canonical.Len() > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(percentEncode(key))
		canonical.WriteByte('=')
		canonical.WriteString(percentEncode(params.Get(key)))
	}
	stringToSign := "GET&%2F&" + percentEncode(canonical.String())
	mac := hmac.New(sha1.New, []byte(a.AccessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	params.Set("Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "urMail DNS onboarding")
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("aliyun dns: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

type aliyunRecords struct {
	DomainRecords struct {
		Record []struct {
			RecordID string `json:"RecordId"`
			RR       string `json:"RR"`
			Type     string `json:"Type"`
			Value    string `json:"Value"`
		} `json:"Record"`
	} `json:"DomainRecords"`
}

// EnsureTXT creates or updates a TXT record in the given zone.
func (a *Aliyun) EnsureTXT(ctx context.Context, zone, name, value string) error {
	rr := strings.TrimSuffix(name, "."+zone)
	if rr == name {
		return fmt.Errorf("record %q is outside zone %q", name, zone)
	}
	raw, err := a.call(ctx, "DescribeDomainRecords", url.Values{
		"DomainName": {zone}, "RRKeyWord": {rr}, "Type": {"TXT"}, "PageSize": {"100"},
	})
	if err != nil {
		return err
	}
	var list aliyunRecords
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	if len(list.DomainRecords.Record) > 0 {
		_, err = a.call(ctx, "UpdateDomainRecord", url.Values{
			"RecordId": {list.DomainRecords.Record[0].RecordID},
			"RR":       {rr}, "Type": {"TXT"}, "Value": {value},
		})
		return err
	}
	_, err = a.call(ctx, "AddDomainRecord", url.Values{
		"DomainName": {zone}, "RR": {rr}, "Type": {"TXT"}, "Value": {value},
	})
	return err
}

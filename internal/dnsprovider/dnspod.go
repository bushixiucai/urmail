package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DNSPod is the Tencent Cloud DNSPod (dnsapi.cn) TXT client.
type DNSPod struct {
	Token  string
	Client *http.Client
	Base   string
}

func NewDNSPod(token string) *DNSPod {
	return &DNSPod{
		Token:  token,
		Client: &http.Client{Timeout: 15 * time.Second},
		Base:   "https://dnsapi.cn",
	}
}

type dnspodStatus struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type dnspodResponse struct {
	Status  dnspodStatus `json:"status"`
	Records []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"records"`
}

func (d *DNSPod) call(ctx context.Context, path string, form url.Values) (dnspodResponse, error) {
	form.Set("login_token", d.Token)
	form.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.Base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return dnspodResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.Client.Do(req)
	if err != nil {
		return dnspodResponse{}, err
	}
	defer resp.Body.Close()
	var out dnspodResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return dnspodResponse{}, err
	}
	if out.Status.Code != "1" {
		return out, fmt.Errorf("dnspod: %s (%s)", out.Status.Message, out.Status.Code)
	}
	return out, nil
}

func (d *DNSPod) EnsureTXT(ctx context.Context, zone, name, value string) error {
	sub := strings.TrimSuffix(name, "."+zone)
	if sub == name {
		return fmt.Errorf("record %q is outside zone %q", name, zone)
	}
	list, err := d.call(ctx, "/Record.List", url.Values{
		"domain": {zone}, "sub_domain": {sub}, "record_type": {"TXT"},
	})
	if err != nil {
		return err
	}
	if len(list.Records) > 0 {
		_, err = d.call(ctx, "/Record.Modify", url.Values{
			"domain": {zone}, "record_id": {list.Records[0].ID},
			"sub_domain": {sub}, "record_type": {"TXT"},
			"record_line": {"默认"}, "value": {value},
		})
		return err
	}
	_, err = d.call(ctx, "/Record.Create", url.Values{
		"domain": {zone}, "sub_domain": {sub}, "record_type": {"TXT"},
		"record_line": {"默认"}, "value": {value},
	})
	return err
}

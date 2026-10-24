package dnsprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Credentials are encrypted at rest. Endpoints are fixed by the provider,
// never taken from tenant input. Legacy DNSPod DToken and Tencent TC3 are
// deliberately separate protocols and credential types.
type Credentials struct {
	Token           string `json:"token,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
}

// Zone is the provider-verified identity to which every operation is bound.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RemoteRecord retains upstream identity for guarded update and rollback.
type RemoteRecord struct {
	ID string `json:"id"`
	Record
	LineID   string `json:"line_id,omitempty"`
	Proxied  bool   `json:"proxied,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// RecordManager implements read-only inspection and explicit planned writes.
type RecordManager interface {
	Probe(context.Context, string) (Zone, error)
	List(context.Context, Zone) ([]RemoteRecord, error)
	Create(context.Context, Zone, Record) (RemoteRecord, error)
	Update(context.Context, Zone, RemoteRecord, Record) (RemoteRecord, error)
	Delete(context.Context, Zone, RemoteRecord) error
}

// ErrProvider intentionally omits upstream response bodies and secrets.
var ErrProvider = errors.New("DNS_PROVIDER_UNAVAILABLE")
var errNoRecords = errors.New("DNS_PROVIDER_EMPTY_RECORD_SET")

// APIManager uses bounded responses, deadlines and a redirect-rejecting client.
type APIManager struct {
	provider    string
	credentials Credentials
	endpoint    string
	client      *http.Client
	now         func() time.Time
}

// NewRecordManager never authorizes a write; callers must independently verify
// tenant ownership, inspect the zone and confirm an immutable change plan.
func NewRecordManager(provider string, credentials Credentials) (*APIManager, error) {
	endpoints := map[string]string{"cloudflare": "https://api.cloudflare.com/client/v4", "aliyun": "https://alidns.aliyuncs.com/", "dnspod": "https://dnsapi.cn", "tencent": "https://dnspod.tencentcloudapi.com"}
	endpoint, ok := endpoints[provider]
	if !ok {
		return nil, errors.New("DNS_PROVIDER_UNSUPPORTED")
	}
	if (provider == "cloudflare" || provider == "dnspod") && strings.TrimSpace(credentials.Token) == "" {
		return nil, errors.New("DNS_CREDENTIAL_INVALID")
	}
	if (provider == "aliyun" || provider == "tencent") && (credentials.AccessKeyID == "" || credentials.AccessKeySecret == "") {
		return nil, errors.New("DNS_CREDENTIAL_INVALID")
	}
	if len(credentials.Token) > 8192 || len(credentials.AccessKeyID) > 256 || len(credentials.AccessKeySecret) > 8192 || strings.ContainsAny(credentials.Token+credentials.AccessKeyID+credentials.AccessKeySecret, "\r\n") {
		return nil, errors.New("DNS_CREDENTIAL_INVALID")
	}
	return &APIManager{provider: provider, credentials: credentials, endpoint: endpoint, client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now}, nil
}
func (m *APIManager) request(ctx context.Context, method, path string, body []byte, headers map[string]string) (map[string]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrProvider
	}
	req.Header.Set("User-Agent", "urMail DNS onboarding")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, ErrProvider
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrProvider
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return nil, ErrProvider
	}
	return result, nil
}
func decodeField[T any](fields map[string]json.RawMessage, key string) (T, error) {
	var value T
	raw, ok := fields[key]
	if !ok || json.Unmarshal(raw, &value) != nil {
		return value, ErrProvider
	}
	return value, nil
}
func (m *APIManager) cloudflare(ctx context.Context, method, path string, payload any) (map[string]json.RawMessage, error) {
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	result, err := m.request(ctx, method, path, raw, map[string]string{"Authorization": "Bearer " + m.credentials.Token, "Content-Type": "application/json", "User-Agent": "urMail DNS onboarding"})
	if err != nil {
		return nil, err
	}
	ok, err := decodeField[bool](result, "success")
	if err != nil || !ok {
		return nil, ErrProvider
	}
	return result, nil
}
func (m *APIManager) rpc(ctx context.Context, action string, params map[string]any) (map[string]json.RawMessage, error) {
	if m.provider == "tencent" {
		return m.tencent(ctx, action, params)
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, fmt.Sprint(v))
	}
	if m.provider == "aliyun" {
		a := NewAliyun(m.credentials.AccessKeyID, m.credentials.AccessKeySecret)
		a.Base = m.endpoint
		a.Client = m.client
		raw, err := a.call(ctx, action, form)
		if err != nil {
			return nil, ErrProvider
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil, ErrProvider
		}
		if _, bad := fields["Code"]; bad {
			return nil, ErrProvider
		}
		return fields, nil
	}
	form.Set("login_token", m.credentials.Token)
	form.Set("format", "json")
	fields, err := m.request(ctx, http.MethodPost, "/"+action, []byte(form.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return nil, err
	}
	status, err := decodeField[dnspodStatus](fields, "status")
	if err == nil && action == "Record.List" && status.Code == "10" {
		return nil, errNoRecords
	}
	if err != nil || status.Code != "1" {
		return nil, ErrProvider
	}
	return fields, nil
}
func shaHex(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func (m *APIManager) tencent(ctx context.Context, action string, payload map[string]any) (map[string]json.RawMessage, error) {
	raw, _ := json.Marshal(payload)
	now := m.now().UTC()
	stamp := strconv.FormatInt(now.Unix(), 10)
	date := now.Format("2006-01-02")
	host := "dnspod.tencentcloudapi.com"
	canonical := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:" + host + "\nx-tc-action:" + strings.ToLower(action) + "\n\ncontent-type;host;x-tc-action\n" + shaHex(raw)
	scope := date + "/dnspod/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + shaHex([]byte(canonical))
	signing := hmacSHA(hmacSHA(hmacSHA([]byte("TC3"+m.credentials.AccessKeySecret), date), "dnspod"), "tc3_request")
	auth := "TC3-HMAC-SHA256 Credential=" + m.credentials.AccessKeyID + "/" + scope + ", SignedHeaders=content-type;host;x-tc-action, Signature=" + hex.EncodeToString(hmacSHA(signing, toSign))
	result, err := m.request(ctx, http.MethodPost, "/", raw, map[string]string{"Authorization": auth, "Content-Type": "application/json; charset=utf-8", "X-TC-Action": action, "X-TC-Timestamp": stamp, "X-TC-Version": "2021-03-23"})
	if err != nil {
		return nil, err
	}
	fields, err := decodeField[map[string]json.RawMessage](result, "Response")
	if err != nil {
		return nil, err
	}
	if _, bad := fields["Error"]; bad {
		return nil, ErrProvider
	}
	return fields, nil
}

// Probe only reads the exact requested zone. Ambiguous or mismatched replies
// fail closed instead of selecting a tenant-inaccessible parent zone.
func (m *APIManager) Probe(ctx context.Context, name string) (Zone, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || strings.ContainsAny(name, "/\\\r\n") {
		return Zone{}, ErrProvider
	}
	if m.provider == "cloudflare" {
		fields, err := m.cloudflare(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(name)+"&per_page=50", nil)
		if err != nil {
			return Zone{}, err
		}
		zones, err := decodeField[[]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}](fields, "result")
		if err != nil || len(zones) != 1 || zones[0].Name != name || zones[0].ID == "" {
			return Zone{}, ErrProvider
		}
		return Zone{zones[0].ID, name}, nil
	}
	action := "DescribeDomainInfo"
	params := map[string]any{"Domain": name}
	if m.provider == "aliyun" {
		params = map[string]any{"DomainName": name}
	}
	if m.provider == "dnspod" {
		action = "Domain.Info"
		params = map[string]any{"domain": name}
	}
	fields, err := m.rpc(ctx, action, params)
	if err != nil {
		return Zone{}, err
	}
	var id, got string
	switch m.provider {
	case "aliyun":
		id, _ = decodeField[string](fields, "DomainId")
		got, _ = decodeField[string](fields, "DomainName")
	case "dnspod":
		info, e := decodeField[struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}](fields, "domain")
		if e != nil {
			return Zone{}, e
		}
		id, got = info.ID, info.Name
	case "tencent":
		info, e := decodeField[struct {
			ID   uint64 `json:"DomainId"`
			Name string `json:"Domain"`
		}](fields, "DomainInfo")
		if e != nil {
			return Zone{}, e
		}
		id, got = strconv.FormatUint(info.ID, 10), info.Name
	}
	if id == "" || id == "0" || strings.ToLower(got) != name {
		return Zone{}, ErrProvider
	}
	return Zone{id, name}, nil
}
func validZoneRecord(zone Zone, record Record) bool {
	return zone.ID != "" && zone.Name != "" && (record.Name == zone.Name || strings.HasSuffix(record.Name, "."+zone.Name)) && !strings.HasPrefix(record.Name, "_urmail-verify.") && record.Type != "" && len(record.Value) <= 16384 && !strings.ContainsAny(record.Name+record.Value, "\r\n")
}
func relativeName(zone Zone, name string) string {
	if name == zone.Name {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone.Name)
}
func fullName(zone Zone, name string) string {
	if name == "@" || name == "" {
		return zone.Name
	}
	return name + "." + zone.Name
}

// List returns at most 5000 records. A larger or unprovably complete zone
// requires manual handling rather than an incomplete destructive diff.
func (m *APIManager) List(ctx context.Context, zone Zone) ([]RemoteRecord, error) {
	var all []RemoteRecord
	for page := 0; page < 50; page++ {
		var batch []RemoteRecord
		var total int
		if m.provider == "cloudflare" {
			fields, err := m.cloudflare(ctx, http.MethodGet, "/zones/"+url.PathEscape(zone.ID)+"/dns_records?per_page=100&page="+strconv.Itoa(page+1), nil)
			if err != nil {
				return nil, err
			}
			rows, err := decodeField[[]struct {
				ID       string     `json:"id"`
				Type     RecordType `json:"type"`
				Name     string     `json:"name"`
				Value    string     `json:"content"`
				Priority int        `json:"priority"`
				TTL      int        `json:"ttl"`
				Proxied  bool       `json:"proxied"`
			}](fields, "result")
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				value := row.Value

				batch = append(batch, RemoteRecord{ID: row.ID, Record: Record{row.Type, strings.TrimSuffix(strings.ToLower(row.Name), "."), value, row.Priority, row.TTL}, Proxied: row.Proxied})
			}
			info, e := decodeField[struct {
				Total int `json:"total_count"`
			}](fields, "result_info")
			if e != nil {
				return nil, e
			}
			total = info.Total
		} else {
			action := "DescribeRecordList"
			params := map[string]any{"Domain": zone.Name, "Offset": page * 100, "Limit": 100}
			if m.provider == "aliyun" {
				action = "DescribeDomainRecords"
				params = map[string]any{"DomainName": zone.Name, "PageNumber": page + 1, "PageSize": 100}
			}
			if m.provider == "dnspod" {
				action = "Record.List"
				params = map[string]any{"domain_id": zone.ID, "offset": page * 100, "length": 100}
			}
			fields, err := m.rpc(ctx, action, params)
			if errors.Is(err, errNoRecords) && page == 0 {
				return []RemoteRecord{}, nil
			}
			if err != nil {
				return nil, err
			}
			switch m.provider {
			case "aliyun":
				rows, e := decodeField[struct {
					Rows []struct {
						ID       string     `json:"RecordId"`
						LineID   string     `json:"Line"`
						Status   string     `json:"Status"`
						Type     RecordType `json:"Type"`
						Name     string     `json:"RR"`
						Value    string     `json:"Value"`
						Priority int        `json:"Priority"`
						TTL      int        `json:"TTL"`
					} `json:"Record"`
				}](fields, "DomainRecords")
				if e != nil {
					return nil, e
				}
				for _, r := range rows.Rows {
					batch = append(batch, RemoteRecord{ID: r.ID, Record: Record{r.Type, fullName(zone, r.Name), r.Value, r.Priority, r.TTL}, LineID: r.LineID, Disabled: strings.EqualFold(r.Status, "DISABLE")})
				}
				total, _ = decodeField[int](fields, "TotalCount")
			case "dnspod":
				rows, e := decodeField[[]struct {
					ID       string     `json:"id"`
					LineID   string     `json:"line_id"`
					Enabled  string     `json:"enabled"`
					Type     RecordType `json:"type"`
					Name     string     `json:"name"`
					Value    string     `json:"value"`
					Priority string     `json:"mx"`
					TTL      string     `json:"ttl"`
				}](fields, "records")
				if e != nil {
					return nil, e
				}
				for _, r := range rows {
					priority, _ := strconv.Atoi(r.Priority)
					ttl, _ := strconv.Atoi(r.TTL)
					batch = append(batch, RemoteRecord{ID: r.ID, Record: Record{r.Type, fullName(zone, r.Name), r.Value, priority, ttl}, LineID: r.LineID, Disabled: r.Enabled == "0"})
				}
				info, e := decodeField[struct {
					Total string `json:"record_total"`
				}](fields, "info")
				if e != nil {
					return nil, e
				}
				total, _ = strconv.Atoi(info.Total)
			default:
				rows, e := decodeField[[]struct {
					ID       uint64     `json:"RecordId"`
					LineID   string     `json:"LineId"`
					Status   string     `json:"Status"`
					Type     RecordType `json:"Type"`
					Name     string     `json:"Name"`
					Value    string     `json:"Value"`
					Priority int        `json:"MX"`
					TTL      int        `json:"TTL"`
				}](fields, "RecordList")
				if e != nil {
					return nil, e
				}
				for _, r := range rows {
					batch = append(batch, RemoteRecord{ID: strconv.FormatUint(r.ID, 10), Record: Record{r.Type, fullName(zone, r.Name), r.Value, r.Priority, r.TTL}, LineID: r.LineID, Disabled: strings.EqualFold(r.Status, "DISABLE")})
				}
				info, e := decodeField[struct {
					Total int `json:"TotalCount"`
				}](fields, "RecordCountInfo")
				if e != nil {
					return nil, e
				}
				total = info.Total
			}
		}
		if total < 0 || total > 5000 || len(all)+len(batch) > 5000 {
			return nil, ErrProvider
		}
		all = append(all, batch...)
		if len(all) == total {
			return all, nil
		}
		if len(batch) == 0 {
			return nil, ErrProvider
		}
	}
	return nil, ErrProvider
}
func (m *APIManager) write(ctx context.Context, zone Zone, old *RemoteRecord, record Record, remove bool) (RemoteRecord, error) {
	if !validZoneRecord(zone, record) {
		return RemoteRecord{}, errors.New("DNS_RECORD_OUTSIDE_AUTHORIZED_ZONE")
	}
	if old != nil && old.ID == "" {
		return RemoteRecord{}, ErrProvider
	}
	if m.provider == "cloudflare" {
		method, path := http.MethodPost, "/zones/"+url.PathEscape(zone.ID)+"/dns_records"
		if old != nil {
			method = http.MethodPut
			path += "/" + url.PathEscape(old.ID)
		}
		if remove {
			method = http.MethodDelete
		}
		payload := map[string]any{"type": record.Type, "name": record.Name, "content": record.Value, "ttl": max(record.TTL, 60), "proxied": false}
		if record.Type == RecordTypeMX {
			payload["priority"] = record.Priority
		}
		fields, err := m.cloudflare(ctx, method, path, payload)
		if err != nil {
			return RemoteRecord{}, err
		}
		row, e := decodeField[struct {
			ID string `json:"id"`
		}](fields, "result")
		if e != nil || row.ID == "" {
			return RemoteRecord{}, ErrProvider
		}
		return RemoteRecord{ID: row.ID, Record: record}, nil
	}
	action := "CreateRecord"
	params := map[string]any{"Domain": zone.Name, "SubDomain": relativeName(zone, record.Name), "RecordType": record.Type, "RecordLine": "默认", "Value": record.Value, "TTL": max(record.TTL, 60), "MX": record.Priority}
	if old != nil {
		action = "ModifyRecord"
		id, err := strconv.ParseUint(old.ID, 10, 64)
		if err != nil {
			return RemoteRecord{}, ErrProvider
		}
		params["RecordId"] = id
	}
	if remove {
		action = "DeleteRecord"
		params = map[string]any{"Domain": zone.Name, "RecordId": params["RecordId"]}
	}
	if m.provider == "aliyun" {
		action = "AddDomainRecord"
		params = map[string]any{"DomainName": zone.Name, "RR": relativeName(zone, record.Name), "Type": record.Type, "Value": record.Value, "TTL": max(record.TTL, 60), "Priority": record.Priority}
		if old != nil {
			action = "UpdateDomainRecord"
			params["RecordId"] = old.ID
			delete(params, "DomainName")
		}
		if remove {
			action = "DeleteDomainRecord"
			params = map[string]any{"RecordId": old.ID}
		}
	}
	if m.provider == "dnspod" {
		action = "Record.Create"
		params = map[string]any{"domain_id": zone.ID, "sub_domain": relativeName(zone, record.Name), "record_type": record.Type, "record_line_id": "10=0", "value": record.Value, "ttl": max(record.TTL, 60), "mx": record.Priority}
		if old != nil {
			action = "Record.Modify"
			params["record_id"] = old.ID
		}
		if remove {
			action = "Record.Remove"
			params = map[string]any{"domain_id": zone.ID, "record_id": old.ID}
		}
	}
	if old != nil && old.LineID != "" && !remove {
		switch m.provider {
		case "aliyun":
			params["Line"] = old.LineID
		case "dnspod":
			delete(params, "record_line")
			params["record_line_id"] = old.LineID
		case "tencent":
			delete(params, "RecordLine")
			params["RecordLineId"] = old.LineID
		}
	}
	fields, err := m.rpc(ctx, action, params)
	if err != nil {
		return RemoteRecord{}, err
	}
	if remove {
		return *old, nil
	}
	id := ""
	if old != nil {
		id = old.ID
	}
	if m.provider == "aliyun" {
		id, _ = decodeField[string](fields, "RecordId")
	}
	if m.provider == "dnspod" && old == nil {
		r, e := decodeField[struct {
			ID string `json:"id"`
		}](fields, "record")
		if e != nil {
			return RemoteRecord{}, e
		}
		id = r.ID
	}
	if m.provider == "tencent" && old == nil {
		n, e := decodeField[uint64](fields, "RecordId")
		if e != nil {
			return RemoteRecord{}, e
		}
		id = strconv.FormatUint(n, 10)
	}
	if id == "" || id == "0" {
		return RemoteRecord{}, ErrProvider
	}
	return RemoteRecord{ID: id, Record: record, LineID: func() string {
		if old != nil {
			return old.LineID
		}
		return ""
	}()}, nil
}

// Create performs one explicitly planned creation.
func (m *APIManager) Create(ctx context.Context, zone Zone, record Record) (RemoteRecord, error) {
	return m.write(ctx, zone, nil, record, false)
}

// Update changes only the supplied exact upstream record identity.
func (m *APIManager) Update(ctx context.Context, zone Zone, old RemoteRecord, record Record) (RemoteRecord, error) {
	if !validZoneRecord(zone, old.Record) {
		return RemoteRecord{}, ErrProvider
	}
	return m.write(ctx, zone, &old, record, false)
}

// Delete supports guarded rollback of proven owned records.
func (m *APIManager) Delete(ctx context.Context, zone Zone, old RemoteRecord) error {
	_, err := m.write(ctx, zone, &old, old.Record, true)
	return err
}

// WithTransport injects a trusted application transport for local contract
// tests. It preserves the fixed provider URL, deadlines and redirect rejection.
func (m *APIManager) WithTransport(transport http.RoundTripper) *APIManager {
	client := *m.client
	client.Transport = transport
	m.client = &client
	return m
}

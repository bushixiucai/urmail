package datamigrate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ConvertValue encodes one PG value into its SQLite storage form per the
// type-map encoding. PG drivers hand back: int64 for bigint/integer/smallint,
// bool for boolean, string for text/uuid/inet/date/numeric, time.Time for
// timestamptz, []byte for bytea, string for jsonb (pgx default) — jsonb may
// also arrive as []byte, both accepted. Arrays arrive as string (pgx simple
// protocol text format '{a,b}') or []byte.
func ConvertValue(cm ColumnMap, v any) (any, error) {
	switch cm.Encoding {
	case EncodingPassthrough:
		switch cm.PGType {
		case "boolean":
			return ConvertBool(v)
		case "uuid":
			return ConvertUUID(v)
		case "date":
			return ConvertDate(v)
		default:
			return v, nil
		}
	case EncodingUnixMicro:
		return ConvertUnixMicro(v)
	case EncodingJSONText:
		return ConvertJSONText(v)
	case EncodingJSONArray:
		return ConvertJSONArray(v)
	default:
		return nil, fmt.Errorf("table %s column %s: unknown encoding %q", cm.Table, cm.Column, cm.Encoding)
	}
}

// ConvertUUID maps a PG uuid to its canonical dashed text form. pgx delivers
// uuid scanning-into-any as [16]uint8 or string.
func ConvertUUID(v any) (any, error) {
	switch u := v.(type) {
	case [16]uint8:
		b := u[:]
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
	case string:
		return u, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("uuid: unsupported %T", v)
	}
}

// ConvertDate maps a PG date to the ISO date text form (SQLite TEXT).
// pgx delivers date scanning-into-any as time.Time (midnight UTC).
func ConvertDate(v any) (any, error) {
	switch d := v.(type) {
	case time.Time:
		return d.Format("2006-01-02"), nil
	case string:
		return d, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("date: unsupported %T", v)
	}
}

// ConvertBool maps PG boolean to 0/1.
func ConvertBool(v any) (any, error) {
	switch b := v.(type) {
	case bool:
		if b {
			return 1, nil
		}
		return 0, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("bool: unsupported %T", v)
	}
}

// Unix micro sentinel values for PG ±infinity timestamps. The mapping is
// collision-free, order-preserving and invertible: every finite PG
// timestamptz maps into [MinFiniteUnixMicro, MaxFiniteUnixMicro]
// (0001-01-01 .. 9999-12-31), strictly inside the int64 range, so
// int64min/-infinity and int64max/+infinity are reserved sentinels that
// preserve ordering (-inf < finite < +inf) and decode back uniquely.
const (
	MinUnixMicroSentinel = int64(-9223372036854775808)
	MaxUnixMicroSentinel = int64(9223372036854775807)
	// MinFiniteUnixMicro is 0001-01-01T00:00:00Z in unix micros; no finite
	// value can collide with the sentinels.
	MinFiniteUnixMicro = int64(-62135596800000000)
	// MaxFiniteUnixMicro is 9999-12-31T23:59:59.999999Z in unix micros.
	MaxFiniteUnixMicro = int64(253402300799999999)
)

// ConvertUnixMicro maps PG timestamptz to unix micros (the §3.1 encoding).
// pgx delivers timestamptz as time.Time or pgtype.Timestamptz (which carries
// NULL and ±infinity); infinity maps to the reserved int64 sentinels so
// ordering semantics survive collision-free.
func ConvertUnixMicro(v any) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t.UnixMicro(), nil
	case pgtype.Timestamptz:
		if !t.Valid {
			return nil, nil
		}
		switch t.InfinityModifier {
		case pgtype.Infinity:
			return MaxUnixMicroSentinel, nil
		case pgtype.NegativeInfinity:
			return MinUnixMicroSentinel, nil
		}
		return t.Time.UnixMicro(), nil
	case pgtype.InfinityModifier:
		if t == pgtype.Infinity {
			return MaxUnixMicroSentinel, nil
		}
		return MinUnixMicroSentinel, nil
	case int64:
		return t, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unix micro: unsupported %T", v)
	}
}

// DecodeUnixMicro inverts ConvertUnixMicro for stored INTEGER values: the
// sentinels decode back to ±infinity markers, everything else returns the
// unix micro integer unchanged (lossless round trip).
func DecodeUnixMicro(v any) (any, error) {
	switch n := v.(type) {
	case int64:
		switch n {
		case MaxUnixMicroSentinel:
			return "infinity", nil
		case MinUnixMicroSentinel:
			return "-infinity", nil
		}
		return n, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("decode unix micro: unsupported %T", v)
	}
}

// ConvertJSONText normalizes jsonb (string, bytes, or the decoded map/slice
// forms pgx v5 produces) to compact canonical JSON text so both sides hash
// identically.
func ConvertJSONText(v any) (any, error) {
	switch s := v.(type) {
	case string:
		// pgx v5 unwraps jsonb scalars: a JSON string arrives WITHOUT its
		// quotes. Re-wrap anything that does not parse as JSON.
		return normalizeJSONOpt([]byte(s), true)
	case []byte:
		return normalizeJSONOpt(s, false)
	case map[string]any, []any:
		b, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("json text marshal: %w", err)
		}
		return normalizeJSONOpt(b, false)
	case float64, float32, int64, int, bool:
		b, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("json scalar marshal: %w", err)
		}
		return normalizeJSONOpt(b, false)
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("json text: unsupported %T", v)
	}
}

// ConvertJSONArray maps a PG text array to the §3.2 JSON array text encoding.
// pgx delivers arrays in text format ('{a,b}') or decoded []any/string forms.
func ConvertJSONArray(v any) (any, error) {
	switch s := v.(type) {
	case string:
		return arrayToJSON(s)
	case []byte:
		return arrayToJSON(string(s))
	case []any:
		b, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("json array marshal: %w", err)
		}
		return string(b), nil
	case []string:
		b, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("json array marshal: %w", err)
		}
		return string(b), nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("json array: unsupported %T", v)
	}
}

func normalizeJSON(raw []byte) (any, error) {
	return normalizeJSONOpt(raw, false)
}

func normalizeJSONOpt(raw []byte, stringFallback bool) (any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", nil
	}
	var parsed any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		// pgx unwraps jsonb scalars: an unparseable payload that arrived as a
		// Go string is a JSON string scalar — re-wrap it with quotes so the
		// canonical form matches the target-side parse.
		if stringFallback {
			wrapped, merr := json.Marshal(string(raw))
			if merr == nil {
				return string(wrapped), nil
			}
		}
		return nil, fmt.Errorf("jsonb decode: %w", err)
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("jsonb re-encode: %w", err)
	}
	return string(out), nil
}

// arrayToJSON converts PG text array format '{a,b}' to JSON ["a","b"].
// Quotes inside PG array literals ("" escapes) are unescaped. Nested arrays
// are not supported by the mapped columns (text[] only).
func arrayToJSON(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return "[]", nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		// NULL::text[] can arrive as the literal "NULL"; treat as empty.
		if strings.EqualFold(s, "NULL") {
			return nil, nil
		}
		return nil, fmt.Errorf("pg array: malformed %q", s)
	}
	inner := s[1 : len(s)-1]
	out := []string{}
	if strings.TrimSpace(inner) == "" {
		b, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	items := splitArrayItems(inner)
	for _, item := range items {
		item = strings.TrimSpace(item)
		item = strings.TrimPrefix(item, `"`)
		item = strings.TrimSuffix(item, `"`)
		item = strings.ReplaceAll(item, `\"`, `"`)
		item = strings.ReplaceAll(item, `\\`, `\`)
		out = append(out, item)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func splitArrayItems(inner string) []string {
	var items []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		if inQuotes {
			cur.WriteByte(ch)
			if ch == '"' && (i == 0 || inner[i-1] != '\\') {
				inQuotes = false
			}
			continue
		}
		switch ch {
		case '"':
			inQuotes = true
			cur.WriteByte(ch)
		case ',':
			items = append(items, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	items = append(items, cur.String())
	return items
}

// canonicalJSON renders a converted row as stable JSON for hashing: columns
// sorted, nulls as null, []byte as base64 string, numbers preserved.
func canonicalJSON(cols []string, vals []any) (string, error) {
	ordered := make([]string, len(cols))
	copy(ordered, cols)
	sortStrings(ordered)
	lookup := map[string]any{}
	for i, c := range cols {
		lookup[c] = vals[i]
	}
	obj := make(map[string]any, len(ordered))
	for _, c := range ordered {
		v := lookup[c]
		if b, ok := v.([]byte); ok {
			v = base64Of(b)
		}
		obj[c] = v
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("canonical json: %w", err)
	}
	return string(out), nil
}

func base64Of(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], b[i:])
		sb.WriteByte(enc[chunk[0]>>2])
		sb.WriteByte(enc[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			sb.WriteByte(enc[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			sb.WriteByte('=')
		}
		if n > 2 {
			sb.WriteByte(enc[chunk[2]&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

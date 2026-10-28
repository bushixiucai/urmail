package webmail

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// IMAP4rev1 mailbox names use modified UTF-7. UTF-8 stays at the API boundary.
func encodeMailboxWire(name string) string {
	var out strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		units := utf16.Encode(run)
		raw := make([]byte, len(units)*2)
		for i, v := range units {
			binary.BigEndian.PutUint16(raw[i*2:], v)
		}
		out.WriteByte('&')
		out.WriteString(strings.ReplaceAll(base64.RawStdEncoding.EncodeToString(raw), "/", ","))
		out.WriteByte('-')
		run = nil
	}
	for _, r := range name {
		if r >= 32 && r <= 126 {
			flush()
			if r == '&' {
				out.WriteString("&-")
			} else {
				out.WriteRune(r)
			}
		} else {
			run = append(run, r)
		}
	}
	flush()
	return out.String()
}

func decodeMailboxWire(name string) string {
	var out strings.Builder
	for len(name) > 0 {
		index := strings.IndexByte(name, '&')
		if index < 0 {
			out.WriteString(name)
			break
		}
		out.WriteString(name[:index])
		name = name[index+1:]
		end := strings.IndexByte(name, '-')
		if end < 0 {
			return ""
		}
		if end == 0 {
			out.WriteByte('&')
			name = name[1:]
			continue
		}
		raw, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(name[:end], ",", "/"))
		if err != nil || len(raw)%2 != 0 {
			return ""
		}
		units := make([]uint16, len(raw)/2)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(raw[i*2:])
		}
		for i := 0; i < len(units); i++ {
			if units[i] >= 0xd800 && units[i] <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return ""
				}
				i++
			} else if units[i] >= 0xdc00 && units[i] <= 0xdfff {
				return ""
			}
		}
		decoded := utf16.Decode(units)
		out.WriteString(string(decoded))
		name = name[end+1:]
	}
	return out.String()
}

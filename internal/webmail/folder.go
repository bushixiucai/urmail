package webmail

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrBadFolderName is returned when a path param cannot be decoded to a folder.
var ErrBadFolderName = errors.New("webmail bad folder name")

// EncodeFolderName returns a URL-path-safe encoding of an IMAP mailbox name.
// Clients should put the result in /folders/{name}/... path segments.
func EncodeFolderName(name string) string {
	return "b64." + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// DecodeFolderName reverses EncodeFolderName. Plain names without the b64.
// prefix are accepted for simple mailboxes like INBOX (PathUnescape first).
func DecodeFolderName(param string) (string, error) {
	param = strings.TrimSpace(param)
	if param == "" {
		return "", ErrBadFolderName
	}
	if strings.HasPrefix(param, "b64.") {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(param, "b64."))
		if err != nil || len(raw) == 0 || !utf8.Valid(raw) {
			return "", ErrBadFolderName
		}
		name := string(raw)
		if strings.ContainsRune(name, '\x00') {
			return "", ErrBadFolderName
		}
		return name, nil
	}
	if strings.ContainsRune(param, '\x00') || !utf8.ValidString(param) {
		return "", ErrBadFolderName
	}
	return param, nil
}

// WithEncodedNames fills NameEnc on each folder for API clients.
func WithEncodedNames(folders []Folder) []Folder {
	out := make([]Folder, len(folders))
	for i, f := range folders {
		f.NameEnc = EncodeFolderName(f.Name)
		out[i] = f
	}
	return out
}

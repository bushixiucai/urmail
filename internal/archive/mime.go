// Package archive provides durable archive copies and explicit retention.
package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Attachment describes a MIME part without exposing filesystem paths.
type Attachment struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// Content is bounded extracted text and attachment metadata. HTML is never executable.
type Content struct {
	MessageID   string
	Subject     string
	From        string
	To          string
	Body        string
	BodyHTML    string
	Attachments []Attachment
	Error       string
}

// ExtractMIME inspects bounded raw bytes without fetching remote resources.
func ExtractMIME(raw []byte) Content {
	if len(raw) > MaxExtraction {
		return Content{MessageID: "archive-" + fmt.Sprintf("%x", sha256.Sum256(raw)), Attachments: []Attachment{}, Error: "extraction_limit"}
	}
	content := extractMIME(raw, nil)
	if content.MessageID == "" {
		digest := sha256.Sum256(raw)
		content.MessageID = "archive-" + hex.EncodeToString(digest[:])
	}
	return content
}

func extractMIME(raw []byte, capture func(Attachment, []byte)) Content {
	result := Content{Attachments: []Attachment{}}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		result.Error = "invalid_message"
		return result
	}
	result.MessageID = strings.TrimSpace(message.Header.Get("Message-ID"))
	decoder := new(mime.WordDecoder)
	decoder.CharsetReader = charset.NewReaderLabel
	result.Subject = message.Header.Get("Subject")
	if text, e := decoder.DecodeHeader(result.Subject); e == nil {
		result.Subject = text
	}
	result.From = message.Header.Get("From")
	result.To = message.Header.Get("To")
	parts := 0
	var body strings.Builder
	var htmlBody strings.Builder
	var safeHTML strings.Builder
	var walk func(mail.Header, io.Reader, int) error
	walk = func(header mail.Header, reader io.Reader, depth int) error {
		parts++
		if depth > 12 || parts > 200 {
			return errors.New("mime_limits")
		}
		media, params, e := mime.ParseMediaType(header.Get("Content-Type"))
		if e != nil {
			media = "text/plain"
			params = map[string]string{}
		}
		switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
		case "base64":
			reader = base64.NewDecoder(base64.StdEncoding, reader)
		case "quoted-printable":
			reader = quotedprintable.NewReader(reader)
		}
		if strings.HasPrefix(media, "multipart/") {
			boundary := params["boundary"]
			if boundary == "" {
				return errors.New("missing_boundary")
			}
			multi := multipart.NewReader(reader, boundary)
			for {
				part, e := multi.NextPart()
				if errors.Is(e, io.EOF) {
					return nil
				}
				if e != nil {
					return errors.New("invalid_multipart")
				}
				if e = walk(mail.Header(part.Header), part, depth+1); e != nil {
					return e
				}
				_ = part.Close()
			}
		}
		if strings.HasPrefix(media, "text/") && params["charset"] != "" {
			decoded, e := charset.NewReaderLabel(params["charset"], reader)
			if e != nil {
				return errors.New("unsupported_charset")
			}
			reader = decoded
		}
		data, e := io.ReadAll(io.LimitReader(reader, MaxExtraction+1))
		if e != nil {
			return errors.New("invalid_encoding")
		}
		if len(data) > MaxExtraction {
			return errors.New("part_limit")
		}
		disposition, attributes, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		name := attributes["filename"]
		if name == "" {
			name = params["name"]
		}
		if disposition == "attachment" || name != "" || !strings.HasPrefix(media, "text/") {
			digest := sha256.Sum256(data)
			attachment := Attachment{Index: len(result.Attachments), Name: bounded(name, 512), MediaType: media, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
			if capture != nil {
				capture(attachment, data)
			}
			result.Attachments = append(result.Attachments, attachment)
			return nil
		}
		if media == "text/html" && len(data) > 1<<20 {
			return errors.New("html_limit")
		}
		if media == "text/html" && htmlBody.Len() < 1<<20 {
			htmlBody.WriteString(bounded(htmlPlainText(data), (1<<20)-htmlBody.Len()))
			if safeHTML.Len() < 1<<20 {
				safeHTML.WriteString(SanitizeHTML(data))
			}
			htmlBody.WriteByte('\n')
		}
		if media == "text/plain" && body.Len() < 1<<20 {
			body.WriteString(bounded(string(data), (1<<20)-body.Len()))
			body.WriteByte('\n')
		}
		return nil
	}
	if err = walk(message.Header, message.Body, 0); err != nil {
		result.Error = err.Error()
	}
	result.Body = body.String()
	result.BodyHTML = bounded(safeHTML.String(), 1<<20)
	if strings.TrimSpace(result.Body) == "" {
		result.Body = htmlBody.String()
	}
	return result
}
func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// ExtractAttachment decodes one bounded part from a verified immutable original.
func ExtractAttachment(raw []byte, index int) (Attachment, []byte, error) {
	var selected Attachment
	var data []byte
	content := extractMIME(raw, func(part Attachment, decoded []byte) {
		if part.Index == index {
			selected = part
			data = append([]byte{}, decoded...)
		}
	})
	if content.Error != "" {
		return selected, nil, errors.New(content.Error)
	}
	if index < 0 || index >= len(content.Attachments) {
		return selected, nil, errors.New("attachment not found")
	}
	return selected, data, nil
}

func htmlPlainText(raw []byte) string {
	if len(raw) > 1<<20 {
		return ""
	}
	node, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	var result strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if result.Len() >= 1<<20 {
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "template", "iframe", "object", "svg":
				return
			}
		}
		if n.Type == html.TextNode {
			result.WriteString(bounded(n.Data, (1<<20)-result.Len()))
			result.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.Join(strings.Fields(result.String()), " ")
}

// SanitizeHTML preserves a small formatting-only allowlist. All attributes,
// remote resources, forms, links, active embeds and scripts are discarded.
func SanitizeHTML(raw []byte) string {
	if len(raw) > 1<<20 {
		return ""
	}
	document, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	allowed := map[string]bool{"p": true, "br": true, "div": true, "span": true, "b": true, "strong": true, "i": true, "em": true, "u": true, "ul": true, "ol": true, "li": true, "blockquote": true, "pre": true, "code": true, "table": true, "thead": true, "tbody": true, "tr": true, "td": true, "th": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true}
	wrapper := &html.Node{Type: html.ElementNode, Data: "div"}
	nodes := 0
	var copySafe func(*html.Node, *html.Node, int)
	copySafe = func(source, parent *html.Node, depth int) {
		nodes++
		if nodes > 20000 || depth > 64 {
			return
		}
		if source.Type == html.TextNode {
			parent.AppendChild(&html.Node{Type: html.TextNode, Data: source.Data})
			return
		}
		if source.Type == html.ElementNode {
			switch source.Data {
			case "script", "style", "head", "template", "iframe", "object", "svg", "math", "form", "input", "button", "textarea", "select", "video", "audio", "img", "link", "meta", "base":
				return
			}
			if allowed[source.Data] {
				node := &html.Node{Type: html.ElementNode, Data: source.Data}
				parent.AppendChild(node)
				parent = node
			}
		}
		for child := source.FirstChild; child != nil; child = child.NextSibling {
			copySafe(child, parent, depth+1)
		}
	}
	copySafe(document, wrapper, 0)
	var out bytes.Buffer
	if err = html.Render(&out, wrapper); err != nil {
		return ""
	}
	return out.String()
}

// ExtractMIMEReader retains full originals while limiting optional indexing.
func ExtractMIMEReader(reader io.ReadSeeker, size int64, digest string) Content {
	_, _ = reader.Seek(0, io.SeekStart)
	defer func() { _, _ = reader.Seek(0, io.SeekStart) }()
	if size > MaxExtraction {
		result := Content{MessageID: "archive-" + digest, Attachments: []Attachment{}, Error: "extraction_limit"}
		if message, err := mail.ReadMessage(io.LimitReader(reader, 64<<10)); err == nil {
			if id := strings.TrimSpace(message.Header.Get("Message-ID")); id != "" {
				result.MessageID = bounded(id, 998)
			}
			result.Subject = bounded(message.Header.Get("Subject"), 4096)
			result.From = bounded(message.Header.Get("From"), 4096)
			result.To = bounded(message.Header.Get("To"), 4096)
		}
		return result
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxExtraction+1))
	if err != nil {
		return Content{MessageID: "archive-" + digest, Attachments: []Attachment{}, Error: "extraction_unavailable"}
	}
	return ExtractMIME(raw)
}

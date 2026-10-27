package carddav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Request document parsing. Only the pieces the implemented reports need are
// decoded; anything else stays unread and unknown reports are rejected.

// propfindReq is a DAV:propfind body. A missing body means allprop.
type propfindReq struct {
	Allprop  *struct{} `xml:"allprop"`
	Propname *struct{} `xml:"propname"`
	Prop     struct {
		Names []xml.Name `xml:",any"`
	} `xml:"prop"`
}

// textMatchReq is a carddav:text-match predicate.
type textMatchReq struct {
	Negate string `xml:"negate-condition,attr"`
	Value  string `xml:",chardata"`
}

// propFilterReq is a carddav:prop-filter for one vCard property.
type propFilterReq struct {
	Name      string        `xml:"name,attr"`
	TextMatch *textMatchReq `xml:"text-match"`
}

// addressbookQueryReq is a carddav:addressbook-query report body.
type addressbookQueryReq struct {
	Prop struct {
		Names []xml.Name `xml:",any"`
	} `xml:"prop"`
	Filter struct {
		PropFilters []propFilterReq `xml:"prop-filter"`
	} `xml:"filter"`
}

// multigetReq is a carddav:addressbook-multiget report body.
type multigetReq struct {
	Prop struct {
		Names []xml.Name `xml:",any"`
	} `xml:"prop"`
	Hrefs []string `xml:"href"`
}

// syncCollectionReq is a DAV:sync-collection report body (RFC 6578).
type syncCollectionReq struct {
	Token string `xml:"sync-token"`
	Level string `xml:"sync-level"`
	Prop  struct {
		Names []xml.Name `xml:",any"`
	} `xml:"prop"`
}

// readBody reads an XML request body of at most 1 MiB.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
}

// parseBody decodes one buffered XML document.
func parseBody(raw []byte, into any) error {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	return dec.Decode(into)
}

// escapeXML escapes text and attribute values in hand-built multistatus XML.
func escapeXML(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

// multistatusBuilder assembles a DAV:multistatus response document.
type multistatusBuilder struct {
	b       strings.Builder
	token   string
	entries int
}

// xmlnsOpen opens the multistatus document.
const xmlnsOpen = "<D:multistatus xmlns:D=\"DAV:\" xmlns:C=\"urn:ietf:params:xml:ns:carddav\">"

func newMultistatus() *multistatusBuilder {
	m := &multistatusBuilder{}
	m.b.WriteString(xml.Header)
	m.b.WriteString(xmlnsOpen)
	return m
}

// addResponse appends one response element from raw property XML pieces.
// Each piece is already namespaced and escaped.
func (m *multistatusBuilder) addResponse(href string, okProps, missingProps []string, status string) {
	m.entries++
	m.b.WriteString("<D:response><D:href>")
	m.b.WriteString(escapeXML(href))
	m.b.WriteString("</D:href>")
	if status != "" {
		m.b.WriteString("<D:status>")
		m.b.WriteString(escapeXML(status))
		m.b.WriteString("</D:status></D:response>")
		return
	}
	if len(okProps) > 0 {
		m.b.WriteString("<D:propstat><D:prop>")
		for _, prop := range okProps {
			m.b.WriteString(prop)
		}
		m.b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>")
	}
	if len(missingProps) > 0 {
		m.b.WriteString("<D:propstat><D:prop>")
		for _, prop := range missingProps {
			m.b.WriteString(prop)
		}
		m.b.WriteString("</D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>")
	}
	m.b.WriteString("</D:response>")
}

// setToken records the DAV:sync-token trailer value.
func (m *multistatusBuilder) setToken(token string) { m.token = token }

// bytes finishes the document.
func (m *multistatusBuilder) bytes() []byte {
	if m.token != "" {
		m.b.WriteString("<D:sync-token>")
		m.b.WriteString(escapeXML(m.token))
		m.b.WriteString("</D:sync-token>")
	}
	m.b.WriteString("</D:multistatus>")
	return []byte(m.b.String())
}

// Property fragments. Local names only; namespaces are fixed on the document.

const (
	propResourcetypeContact = "<D:resourcetype/>"
	propResourcetypeBook    = "<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>"
	propResourcetypeRoot    = "<D:resourcetype><D:collection/><D:principal/></D:resourcetype>"
	propContentTypeCard     = "<D:getcontenttype>text/vcard; charset=utf-8</D:getcontenttype>"
)

func propGetetag(version int64) string {
	return "<D:getetag>&quot;" + strconv.FormatInt(version, 10) + "&quot;</D:getetag>"
}

func propDisplayName(name string) string {
	return "<D:displayname>" + escapeXML(name) + "</D:displayname>"
}

func propAddressData(card string) string {
	return "<C:address-data>" + escapeXML(card) + "</C:address-data>"
}

func propHref(name string) string {
	return "<D:href>" + escapeXML(name) + "</D:href>"
}

// propSupportedReportSet lists exactly the reports this server implements
// as one DAV:supported-report-set property fragment.
func propSupportedReportSet() string {
	return "<D:supported-report-set>" +
		"<D:supported-report><D:report><C:addressbook-query/></D:report></D:supported-report>" +
		"<D:supported-report><D:report><C:addressbook-multiget/></D:report></D:supported-report>" +
		"<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>" +
		"</D:supported-report-set>"
}

func currentUserPrincipal(href string) string {
	return "<D:current-user-principal><D:href>" + escapeXML(href) + "</D:href></D:current-user-principal>"
}

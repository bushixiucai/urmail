package autoconfig

import (
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	outlookRequestSchema  = "http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006"
	outlookResponseSchema = "http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a"
	mobileRequestSchema   = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006"
	mobileResponseSchema  = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006"
)

// MobileAutodiscoverXML follows MS-ASCMD's MobileSync response contract.
// It deliberately makes no MAPI, EWS or certificate-enrollment promise.
func MobileAutodiscoverXML(cfg Config, email string) string {
	cfg = cfg.WithDefaults()
	if disablePath(cfg.EASPath) {
		return ""
	}
	domain, _ := ParseEmailDomain(email)
	host := cfg.HostForDomain(domain)
	endpoint := xmlEscape("https://" + host + cfg.EASPath)
	return "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
		"<Autodiscover xmlns=\"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006\">\n" +
		"  <Response xmlns=\"" + mobileResponseSchema + "\">\n" +
		"    <Culture>en:us</Culture>\n    <User><DisplayName>" + xmlEscape(email) + "</DisplayName><EMailAddress>" + xmlEscape(email) + "</EMailAddress></User>\n" +
		"    <Action><Settings><Server><Type>MobileSync</Type><Url>" + endpoint + "</Url><Name>" + endpoint + "</Name></Server></Settings></Action>\n" +
		"  </Response>\n</Autodiscover>\n"
}

type discoveryRequest struct {
	XMLName xml.Name
	Request struct {
		Email  string `xml:"EMailAddress"`
		Schema string `xml:"AcceptableResponseSchema"`
	} `xml:"Request"`
}

func discoverHandler(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		media, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
		var email string
		mobile := false
		if media == "text/xml" || media == "application/xml" {
			var request discoveryRequest
			decoder := xml.NewDecoder(c.Request.Body)
			if err := decoder.Decode(&request); err != nil {
				c.Status(400)
				return
			}
			for {
				token, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				whitespace, ok := token.(xml.CharData)
				if err != nil || !ok || strings.TrimSpace(string(whitespace)) != "" {
					c.Status(400)
					return
				}
			}
			if request.XMLName.Local != "Autodiscover" {
				c.Status(400)
				return
			}
			switch request.XMLName.Space {
			case mobileRequestSchema:
				if request.Request.Schema != mobileResponseSchema {
					c.Status(400)
					return
				}
				mobile = true
			case outlookRequestSchema:
				if request.Request.Schema != outlookResponseSchema {
					c.Status(400)
					return
				}
			default:
				c.Status(400)
				return
			}
			email = strings.TrimSpace(request.Request.Email)
			parsed, err := mail.ParseAddress(email)
			if err != nil || parsed.Address != email || len(email) > 320 || strings.ContainsAny(email, "\r\n") {
				c.Status(400)
				return
			}
			if _, valid := ParseEmailDomain(email); !valid {
				c.Status(400)
				return
			}
		} else {
			// Retain the legacy JSON/form discovery input. Real protocol clients use
			// the strict, namespaced XML Request above; never guess their schema.
			var request struct {
				Email string `json:"EmailAddress" form:"emailaddress"`
			}
			if err := c.ShouldBind(&request); err != nil && !errors.Is(err, io.EOF) {
				c.Status(400)
				return
			}
			email = request.Email
		}
		response := OutlookAutodiscoverXML(cfg, email)
		if mobile {
			if disablePath(cfg.WithDefaults().EASPath) {
				c.Status(http.StatusNotFound)
				return
			}
			response = MobileAutodiscoverXML(cfg, email)
		}
		c.Data(http.StatusOK, "text/xml; charset=utf-8", []byte(response))
	}
}

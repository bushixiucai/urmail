// Package pdf renders commercial statements with embedded, subsetted CJK fonts.
package pdf

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-pdf/fpdf"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/i18n"
)

//go:embed fonts/*.gz
var fonts embed.FS
var fontOnce sync.Once
var fontBytes []byte
var fontGlyphs map[string][]int
var fontErr error
var renderSlots = make(chan struct{}, 4)

// ErrBusy means the bounded PDF renderer is handling its maximum concurrency.
var ErrBusy = errors.New("PDF renderer busy")

// InvoiceData contains persisted fields only; rendering never reads live profiles.
type InvoiceData struct {
	Number, Tenant, Amount, Currency, PeriodStart, PeriodEnd, Status, IssuedAt string
	Snapshot                                                                   *billing.DocumentSnapshot
	CreatedAt                                                                  time.Time
	SourcePaymentID                                                            string
	RefundedCents                                                              int64
}

func loadFonts() {
	for _, item := range []struct {
		name    string
		metrics bool
	}{{"fonts/NotoSansSC-Regular.ttf.gz", false}, {"fonts/NotoSansSC-metrics.json.gz", true}} {
		raw, err := fonts.ReadFile(item.name)
		if err != nil {
			fontErr = err
			return
		}
		z, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			fontErr = err
			return
		}
		data, err := io.ReadAll(io.LimitReader(z, 20<<20))
		_ = z.Close()
		if err != nil {
			fontErr = err
			return
		}
		if item.metrics {
			var m struct {
				Glyphs map[string][]int `json:"glyphs"`
			}
			fontErr = json.Unmarshal(data, &m)
			fontGlyphs = m.Glyphs
		} else {
			fontBytes = data
		}
	}
}

// Money formats integer minor units without a floating-point conversion.
func Money(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// Invoice returns a bounded A4 document with automatic, Unicode-aware wrapping.
func Invoice(data InvoiceData) ([]byte, error) {
	select {
	case renderSlots <- struct{}{}:
		defer func() { <-renderSlots }()
	default:
		return nil, ErrBusy
	}
	fontOnce.Do(loadFonts)
	if fontErr != nil {
		return nil, fontErr
	}
	catalogs, err := i18n.NewStore()
	if err != nil {
		return nil, err
	}
	locale := "zh-CN"
	if data.Snapshot != nil && data.Snapshot.Locale == "en" {
		locale = "en"
	}
	catalog := catalogs.Get(locale)
	tr := func(key string) string { return catalog.Lookup("invoice_pdf."+key, nil) }
	escaped := false
	safe := func(s string) string {
		var out strings.Builder
		for _, r := range s {
			if r == '\n' {
				out.WriteRune(r)
				continue
			}
			if r == '\r' || r == '\t' {
				out.WriteRune(' ')
				continue
			}
			if unicode.IsControl(r) {
				continue
			}
			if _, ok := fontGlyphs[strconv.Itoa(int(r))]; r > 0xffff || !ok {
				fmt.Fprintf(&out, "\\u{%X}", r)
				escaped = true
			} else {
				out.WriteRune(r)
			}
		}
		return out.String()
	}
	doc := fpdf.New("P", "mm", "A4", "")
	doc.SetMargins(18, 18, 18)
	doc.SetAutoPageBreak(true, 20)
	doc.SetCatalogSort(true)
	stamp := data.CreatedAt
	if stamp.IsZero() {
		stamp = time.Unix(0, 0).UTC()
	}
	doc.SetCreationDate(stamp)
	doc.SetModificationDate(stamp)
	doc.SetTitle("Commercial statement "+data.Number, true)
	doc.SetAuthor("urMail.cn", true)
	doc.AddUTF8FontFromBytes("NotoSansSC", "", fontBytes)
	doc.SetFont("NotoSansSC", "", 10)
	doc.SetFooterFunc(func() {
		doc.SetY(-14)
		doc.SetFont("NotoSansSC", "", 8)
		doc.SetTextColor(100, 100, 100)
		doc.CellFormat(0, 6, tr("page")+" "+strconv.Itoa(doc.PageNo()), "", 0, "R", false, 0, "")
	})
	doc.AddPage()
	line := func(label, value string) {
		doc.SetFont("NotoSansSC", "", 10)
		doc.SetTextColor(30, 41, 59)
		text := value
		if label != "" {
			text = label + ": " + value
		}
		doc.MultiCell(0, 6, safe(text), "", "L", false)
	}
	section := func(title string) {
		doc.Ln(5)
		doc.SetFont("NotoSansSC", "", 13)
		doc.SetTextColor(15, 23, 42)
		doc.MultiCell(0, 8, safe(title), "B", "L", false)
		doc.Ln(2)
	}
	doc.SetFont("NotoSansSC", "", 21)
	doc.MultiCell(0, 12, tr("title"), "", "L", false)
	doc.SetFont("NotoSansSC", "", 9)
	doc.MultiCell(0, 6, tr("notice"), "", "L", false)
	doc.Ln(5)
	line(tr("number"), data.Number)
	status := data.Status
	if status == "paid" || status == "pending" || status == "void" {
		status = tr(status)
	}
	line(tr("status"), status)
	line(tr("period"), data.PeriodStart+" / "+data.PeriodEnd)
	line(tr("issued"), data.IssuedAt)
	party := func(label string, p billing.DocumentParty) {
		section(label)
		name := p.Name
		if name == "" {
			name = tr("missing")
		}
		line(label, name)
		for _, field := range []struct{ k, v string }{{"tax_id", p.TaxID}, {"address", p.Address}, {"phone", p.Phone}, {"email", p.Email}, {"bank_name", p.BankName}, {"bank_account", p.BankAccount}} {
			if field.v != "" {
				line(tr(field.k), field.v)
			}
		}
	}
	if data.Snapshot != nil {
		party(tr("seller"), data.Snapshot.Seller)
		party(tr("buyer"), data.Snapshot.Buyer)
		if data.Snapshot.BuyerSource == "workspace_name" {
			line(tr("buyer"), tr("workspace"))
		}
	} else {
		section(tr("buyer"))
		line(tr("buyer"), data.Tenant)
	}
	section(tr("total"))
	line(tr("total"), data.Currency+" "+data.Amount)
	if data.Snapshot != nil && data.Snapshot.Amounts.TaxConfigured && data.Snapshot.Amounts.NetCents != nil && data.Snapshot.Amounts.TaxCents != nil && data.Snapshot.Amounts.RateBasisPoints != nil {
		a := data.Snapshot.Amounts
		line(tr("net"), data.Currency+" "+Money(*a.NetCents))
		line(tr("tax"), data.Currency+" "+Money(*a.TaxCents))
		line(tr("rate"), Money(*a.RateBasisPoints)+"%")
	} else {
		line(tr("tax"), tr("tax_unknown"))
	}
	if data.SourcePaymentID != "" {
		line(tr("payment"), data.SourcePaymentID)
	} else {
		line(tr("payment"), tr("no_receipt"))
	}
	if data.RefundedCents > 0 {
		line(tr("refund"), data.Currency+" "+Money(data.RefundedCents))
	}
	doc.Ln(5)
	if data.Snapshot == nil {
		line("", tr("legacy"))
	} else {
		s := data.Snapshot
		line(tr("captured"), s.CapturedAt.UTC().Format(time.RFC3339))
		line("", tr(s.Provenance))
	}
	if escaped {
		doc.Ln(3)
		line("", tr("unicode"))
	}
	var out bytes.Buffer
	if err := doc.Output(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

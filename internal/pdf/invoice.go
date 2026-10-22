// Package pdf renders small, self-contained PDF invoices without external
// dependencies. Only the invoice layout and Helvetica text are supported.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
)

type InvoiceData struct {
	Number      string
	Tenant      string
	Amount      string
	Currency    string
	PeriodStart string
	PeriodEnd   string
	Status      string
	IssuedAt    string
}

func escape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "(", "\\(")
	value = strings.ReplaceAll(value, ")", "\\)")
	return value
}

// Invoice returns a single-page A4 PDF.
func Invoice(data InvoiceData) ([]byte, error) {
	lines := []string{
		"urMail.cn invoice",
		"",
		"Invoice: " + data.Number,
		"Tenant: " + data.Tenant,
		"Amount: " + data.Amount + " " + data.Currency,
		"Period: " + data.PeriodStart + " - " + data.PeriodEnd,
		"Status: " + data.Status,
		"Issued: " + data.IssuedAt,
	}
	var content strings.Builder
	content.WriteString("BT\n/F1 14 Tf\n72 760 Td\n")
	for i, line := range lines {
		if i > 0 {
			content.WriteString("0 -24 Td\n")
		}
		content.WriteString("(" + escape(line) + ") Tj\n")
	}
	content.WriteString("ET\n")
	stream := content.String()

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 6)
	writeObj := func(id int, body string) {
		offsets[id] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", id, body)
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	writeObj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	writeObj(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	xref := b.Len()
	b.WriteString("xref\n0 6\n")
	b.WriteString("0000000000 65535 f \n")
	for id := 1; id <= 5; id++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[id])
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref))
	return b.Bytes(), nil
}

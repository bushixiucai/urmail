package pdf

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
)

func TestCommercialStatementChineseSnapshotAndStableBytes(t *testing.T) {
	stamp := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	snapshot, err := billing.BuildDocumentSnapshot(billing.DocumentProfile{Party: billing.DocumentParty{Name: "示例邮件科技有限公司", TaxID: "91310000EXAMPLE", Address: strings.Repeat("上海市示例路一百号三楼，", 6), BankName: "示例银行", BankAccount: "6222000012345678"}, Tax: billing.DocumentTaxPolicy{Configured: true, RateBasisPoints: 600, IncludedInTotal: true}}, billing.DocumentProfile{Party: billing.DocumentParty{Name: "购买方测试企业𠮷", Address: "北京市示例区测试街 42 号"}}, 10600, "CNY", "quote", stamp)
	if err != nil {
		t.Fatal(err)
	}
	data := InvoiceData{Number: "INV-2026-000042", Amount: "106.00", Currency: "CNY", PeriodStart: "2026-10-24T08:00:00Z", PeriodEnd: "2026-11-24T08:00:00Z", Status: "void", IssuedAt: stamp.Format(time.RFC3339), CreatedAt: stamp, Snapshot: &snapshot, SourcePaymentID: "42", RefundedCents: 10600}
	first, err := Invoice(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Invoice(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("persisted data did not produce stable bytes")
	}
	if len(first) > 2<<20 {
		t.Fatalf("font subset too large: %d", len(first))
	}
	root := t.TempDir()
	path := filepath.Join(root, "statement.pdf")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	if tool, err := exec.LookPath("pdftotext"); err == nil {
		out, err := exec.CommandContext(context.Background(), tool, "-layout", path, "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"商业账单", "示例邮件科技有限公司", "100.00", "6.00", "已作废", `\u{20BB7}`} {
			if !strings.Contains(string(out), want) {
				t.Fatalf("missing text %q: %s", want, out)
			}
		}
	}
	if output := os.Getenv("URMAIL_PDF_EVIDENCE_DIR"); output != "" {
		if err := os.MkdirAll(output, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "commercial-zh-CN.pdf"), first, 0600); err != nil {
			t.Fatal(err)
		}
		snapshot.Locale = "en"
		data.Status = "paid"
		data.RefundedCents = 0
		raw, err := Invoice(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "commercial-en.pdf"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		snapshot.Seller.Address = strings.Repeat("上海市示例街道", 90)
		snapshot.Buyer.Address = strings.Repeat("北京市测试街道", 90)
		snapshot.Seller.BankName = strings.Repeat("示例银行", 45)
		snapshot.Buyer.BankName = strings.Repeat("测试银行", 45)
		raw, err = Invoice(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "commercial-multipage.pdf"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyStatementDoesNotInventTaxOrIdentity(t *testing.T) {
	raw, err := Invoice(InvoiceData{Number: "OLD-1", Tenant: "tenant-1", Amount: "0.00", Currency: "USD", Status: "paid"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("/FontFile2")) {
		t.Fatal("missing embedded font")
	}
}

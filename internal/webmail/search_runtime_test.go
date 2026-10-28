package webmail_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestAdvancedSearchDovecotRuntime(t *testing.T) {
	path := os.Getenv("URMAIL_SEARCH_RUNTIME_FIXTURE")
	if path == "" {
		t.Skip("requires isolated installed Dovecot fixture")
	}
	var fixture struct{ Address, Password string }
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cert, err := os.ReadFile(os.Getenv("URMAIL_SEARCH_RUNTIME_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("invalid CA")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, err := (webmail.NetDialer{Addr: "127.0.0.1:993", TLS: &tls.Config{RootCAs: roots, ServerName: os.Getenv("URMAIL_SEARCH_RUNTIME_HOST"), MinVersion: tls.VersionTLS12}}).Dial(ctx, fixture.Address, fixture.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	marker := "advanced-search-" + time.Now().UTC().Format("150405.000000")
	drafts := make([]webmail.DraftSaveResult, 0, 2)
	for _, attachment := range []bool{false, true} {
		outgoing := webmail.Outgoing{To: []string{fixture.Address}, Subject: marker, Text: "invoice metadata body"}
		if attachment {
			outgoing.Attachments = []webmail.OutgoingAttachment{{Filename: "evidence.bin", ContentType: "application/octet-stream", Content: []byte{1, 2, 3, 0, 255}}}
		}
		draft, saveErr := client.SaveDraft(ctx, fixture.Address, outgoing, 0, 0)
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		drafts = append(drafts, draft)
	}
	defer func() {
		for _, d := range drafts {
			_ = client.DeleteMessages(context.WithoutCancel(ctx), d.Folder, []uint32{d.UID}, true)
		}
	}()
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	for _, tc := range []struct {
		query string
		total int
	}{
		{"subject:" + marker, 2}, {"subject:" + marker + " has:attachment", 1},
		{"subject:" + marker + " since:" + today + " before:" + tomorrow + " to:" + fixture.Address + " body:invoice has:attachment", 1},
		{"subject:" + marker + " before:" + today, 0},
	} {
		result, listErr := client.ListMessages(ctx, drafts[0].Folder, webmail.ListOpts{Query: tc.query, Limit: 1})
		if listErr != nil || result.Total != tc.total {
			t.Fatalf("%s: total=%d err=%v", tc.query, result.Total, listErr)
		}
		if strings.Contains(tc.query, "has:attachment") && tc.total == 1 && (len(result.Messages) != 1 || result.Messages[0].UID != drafts[1].UID) {
			t.Fatal("attachment filter selected incorrect MIME message")
		}
	}
	t.Log("PASS real Dovecot BODYSTRUCTURE nested MIME filter, date inclusion/exclusion, TO/BODY conjunction, exact pagination total; own fixture drafts removed")
}

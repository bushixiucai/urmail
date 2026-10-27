package eas

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// mustEncode panics on encode failure inside fixtures.
func mustEncode(t *testing.T, n *Node) []byte {
	t.Helper()
	return Encode(n)
}

// TestEncodeDecodeFolderSyncRequest round-trips a representative FolderSync
// request (code page 7 only).
func TestEncodeDecodeFolderSyncRequest(t *testing.T) {
	req := Element(pageFolderHierarchy, tokFolderSync, // FolderSync
		Text(pageFolderHierarchy, tokFSyncKey, "0"))
	raw := mustEncode(t, req)
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != pageFolderHierarchy || got.Token != tokFolderSync {
		t.Fatalf("root %v", got.Name())
	}
	if got.ChildText(tokFSyncKey) != "0" {
		t.Fatalf("sync key %q", got.ChildText(tokFSyncKey))
	}
	again := mustEncode(t, got)
	if !bytes.Equal(raw, again) {
		t.Fatalf("not canonical: %x vs %x", raw, again)
	}
}

// TestEncodeDecodeSyncCollectionRequest round-trips a Sync collection
// request across code pages 0 and 17.
func TestEncodeDecodeSyncCollectionRequest(t *testing.T) {
	appData := Element(pageAirSync, tokApplicationData,
		OpaqueNode(17, 0x06, []byte("body-bytes")))
	coll := Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "7"),
		Text(pageAirSync, tokCollectionID, "e:INBOX"),
		Element(pageAirSync, 0x07, // Add
			Text(pageAirSync, tokServerID, "42"),
			appData))
	req := Element(pageAirSync, tokSync,
		Text(pageAirSync, tokWindowSize, "100"),
		Element(pageAirSync, tokCollections, coll))
	raw := mustEncode(t, req)
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	colls := got.Child(tokCollections)
	if colls == nil || len(colls.Children) != 1 {
		t.Fatal("collections lost")
	}
	c := colls.Children[0]
	if c.ChildText(tokSyncKey) != "7" || c.ChildText(tokCollectionID) != "e:INBOX" {
		t.Fatalf("collection %+v", c)
	}
	add := c.Child(0x07)
	if add == nil || add.ChildText(tokServerID) != "42" {
		t.Fatal("add lost")
	}
	data := add.Child(tokApplicationData).Child(0x06)
	if data == nil || !bytes.Equal(data.Opaque, []byte("body-bytes")) {
		t.Fatal("opaque body lost")
	}
	if !bytes.Equal(raw, mustEncode(t, got)) {
		t.Fatal("round-trip not canonical")
	}
}

// TestEncodeDecodePingRequest covers code page 13.
func TestEncodeDecodePingRequest(t *testing.T) {
	folder := Element(pagePing, tokPFolder,
		Text(pagePing, tokPId, "e:INBOX"),
		Text(pagePing, tokPClass, "Email"),
		Text(pagePing, tokPFolderType, "2"))
	req := Element(pagePing, tokPing,
		Text(pagePing, tokHeartbeat, "900"),
		Element(pagePing, tokPFolders, folder))
	got, err := Decode(mustEncode(t, req))
	if err != nil {
		t.Fatal(err)
	}
	if got.ChildText(tokHeartbeat) != "900" {
		t.Fatal("heartbeat lost")
	}
	f := got.Child(tokPFolders).Child(tokPFolder)
	if f.ChildText(tokPId) != "e:INBOX" || f.ChildText(tokPClass) != "Email" {
		t.Fatal("folder lost")
	}
}

// TestEncodeDecodeProvisionRequest covers code page 14 including the
// EASProvisionDoc policy data subtree.
func TestEncodeDecodeProvisionRequest(t *testing.T) {
	policy := Element(pageProvision, tokPolicy,
		Text(pageProvision, tokPolicyType, policyTypeEAS),
		Text(pageProvision, tokPolicyKey, "0"))
	req := Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicies, policy))
	got, err := Decode(mustEncode(t, req))
	if err != nil {
		t.Fatal(err)
	}
	p := got.Child(tokPolicies).Child(tokPolicy)
	if p.ChildText(tokPolicyType) != policyTypeEAS {
		t.Fatal("policy type lost")
	}
	resp := Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicy,
			Text(pageProvision, tokProvStatus, provSuccess),
			Text(pageProvision, tokPolicyKey, "1234"),
			permissiveEASDoc()))
	if _, err = Decode(mustEncode(t, resp)); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeGoldenBytes pins the exact byte encoding (golden fixture) for a
// minimal FolderSync request so wire-format drift is caught.
func TestDecodeGoldenBytes(t *testing.T) {
	golden := "03016a00 0007 56 52 03 30 00 01 01"
	golden = hexCompact(golden)
	raw, err := hex.DecodeString(golden)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if TokenName(got.Page, got.Token) != "FolderSync" {
		t.Fatalf("root %s", TokenName(got.Page, got.Token))
	}
	if got.ChildText(tokFSyncKey) != "0" {
		t.Fatalf("sync key %q", got.ChildText(tokFSyncKey))
	}
	// Canonical re-encode must reproduce the golden bytes exactly.
	if !bytes.Equal(raw, mustEncode(t, got)) {
		t.Fatalf("re-encode %x != golden %x", mustEncode(t, got), raw)
	}
}

// hexCompact strips whitespace from fixture hex strings.
func hexCompact(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// TestDecodeRejectsUnknownPageAndToken ensures garbage is a hard error.
func TestDecodeRejectsUnknownPageAndToken(t *testing.T) {
	// Page switch to 99 (not a registered page).
	if _, err := Decode([]byte{0x03, 0x01, 0x6A, 0x00, 0x00, 99, 0x05, 0x01}); err == nil {
		t.Fatal("unknown page accepted")
	}
	// Known page, unassigned token 0x99.
	if _, err := Decode([]byte{0x03, 0x01, 0x6A, 0x00, 0x07, 0x99, 0x01}); err == nil {
		t.Fatal("unknown token accepted")
	}
	// Truncated header.
	if _, err := Decode([]byte{0x03, 0x01}); err == nil {
		t.Fatal("short header accepted")
	}
}

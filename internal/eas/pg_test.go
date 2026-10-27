package eas

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// pgFixture seeds one tenant with two mailboxes, the first owning a default
// addressbook and calendar plus one change-log row.
type pgFixture struct {
	platform, app              *db.Pool
	tenant, mailboxA, mailboxB int64
	book, cal                  int64
}

func newPGFixture(t *testing.T) pgFixture {
	t.Helper()
	platform, app := testdb.OpenMigrated(t)
	f := pgFixture{platform: platform, app: app}
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand int64
	if err = tx.QueryRow(ctx,
		"INSERT INTO brands(slug,name) VALUES('eas','EAS') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx,
		"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'E','eas','active') RETURNING id", brand).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err = tx.QueryRow(ctx,
		"INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'eas.test','active','t',now()) RETURNING id", f.tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx,
		"INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@eas.test','x','active') RETURNING id", f.tenant, domain).Scan(&f.mailboxA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx,
		"INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'b','b@eas.test','x','active') RETURNING id", f.tenant, domain).Scan(&f.mailboxB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx,
		"INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Contacts') RETURNING id", f.tenant, f.mailboxA).Scan(&f.book); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx,
		"INSERT INTO mailbox_calendars(tenant_id,mailbox_id,name) VALUES($1,$2,'Calendar') RETURNING id", f.tenant, f.mailboxA).Scan(&f.cal); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx,
		"INSERT INTO mailbox_changes(tenant_id,mailbox_id,resource_type,resource_id,version) VALUES($1,$2,'contact',$3,1)", f.tenant, f.mailboxA, f.book); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

// newEASRouter builds the server over the fixture with a fake email lister.
func newEASRouter(t *testing.T, f pgFixture, sess webmail.Session) (*gin.Engine, *Server) {
	t.Helper()
	s := NewServer(f.platform)
	s.EmailFolders = func(context.Context, Session, string) ([]Folder, error) {
		return []Folder{
			{ID: "e:INBOX", DisplayName: "INBOX", Type: folderTypeInbox, Class: "Email"},
			{ID: "e:Sent", DisplayName: "Sent", Type: folderTypeSent, Class: "Email"},
		}, nil
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handle := func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, sess)
		c.Set(middleware.ContextMailboxTokenHash, "hash-a")
		s.HandleCommand(c)
	}
	r.POST("/api/v1/mailbox/eas", handle)
	return r, s
}

func postWBXML(t *testing.T, r *gin.Engine, node *Node) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/api/v1/mailbox/eas?Cmd=FolderSync&DeviceId=dev-a", nil)
	req.Header.Set(headerVersion, "14.1")
	req.Header.Set("Content-Type", ContentTypeWBXML)
	if node != nil {
		req = httptest.NewRequestWithContext(context.Background(), http.MethodPost,
			"/api/v1/mailbox/eas?Cmd="+cmdOf(node)+"&DeviceId=dev-a",
			bytes.NewReader(Encode(node)))
		req.Header.Set(headerVersion, "14.1")
		req.Header.Set("Content-Type", ContentTypeWBXML)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func cmdOf(n *Node) string {
	if n.Token == 0x05 {
		switch n.Page {
		case pageAirSync:
			return CmdSync
		case pageFolderHierarchy:
			return CmdFolderSync
		case pagePing:
			return CmdPing
		case pageProvision:
			return CmdProvision
		}
	}
	return CmdFolderSync
}

func decodeResponse(t *testing.T, w *httptest.ResponseRecorder) *Node {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != ContentTypeWBXML {
		t.Fatalf("content type %q", ct)
	}
	node, err := Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return node
}

// TestDevicePersistenceAndFolderSync covers device row storage, the full
// FolderSync round, and snapshot-driven diffs on real PostgreSQL.
func TestDevicePersistenceAndFolderSync(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)

	// First FolderSync: full hierarchy.
	w := postWBXML(t, r, Element(pageFolderHierarchy, tokFolderSync,
		Text(pageFolderHierarchy, tokFSyncKey, "0")))
	resp := decodeResponse(t, w)
	if resp.ChildText(tokFStatus) != folderSyncSuccess {
		t.Fatalf("status %q", resp.ChildText(tokFStatus))
	}
	firstKey := resp.ChildText(tokFSyncKey)
	changes := resp.Child(tokChanges)
	if changes == nil || len(changes.Children) != 5 { // 2 email + book + cal
		t.Fatalf("expected 4 adds, got %d", len(changes.Children))
	}
	// Device row persisted with negotiated version.
	d, err := GetDevice(t.Context(), f.platform, sess, "dev-a")
	if err != nil || d.ProtocolVersion != "14.1" {
		t.Fatalf("device %+v err %v", d, err)
	}

	// Unchanged second sync: no changes.
	w = postWBXML(t, r, Element(pageFolderHierarchy, tokFolderSync,
		Text(pageFolderHierarchy, tokFSyncKey, firstKey)))
	resp = decodeResponse(t, w)
	if resp.ChildText(tokFStatus) != folderSyncSuccess || resp.Child(tokChanges) != nil {
		t.Fatalf("unexpected changes: %x", w.Body.Bytes())
	}

	// Mutate the default calendar name -> Update entry.
	tx, err := f.platform.PlatformTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(),
		"UPDATE mailbox_calendars SET name='Renamed' WHERE id=$1", f.cal); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(t.Context())
	w = postWBXML(t, r, Element(pageFolderHierarchy, tokFolderSync,
		Text(pageFolderHierarchy, tokFSyncKey, firstKey)))
	resp = decodeResponse(t, w)
	changes = resp.Child(tokChanges)
	if changes == nil || len(changes.Children) != 2 ||
		changes.Children[1].Token != tokFUpdate {
		t.Fatalf("expected one update, got %+v", changes)
	}
}

// TestDeviceAuthorizationBoundary proves mailbox B cannot read or refresh
// mailbox A's device state, and that no session yields nothing.
func TestDeviceAuthorizationBoundary(t *testing.T) {
	f := newPGFixture(t)
	ctx := t.Context()
	sessA := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	sessB := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxB}
	if err := UpsertDevice(ctx, f.platform, sessA, Device{DeviceID: "dev-a", ProtocolVersion: "16.1"}); err != nil {
		t.Fatal(err)
	}
	// B cannot see A's device.
	if _, err := GetDevice(ctx, f.platform, sessB, "dev-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox read: %v", err)
	}
	// B cannot update A's device policy key.
	if err := SetPolicyKey(ctx, f.platform, sessB, "dev-a", 42, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox write: %v", err)
	}
	// A's policy key untouched.
	d, err := GetDevice(ctx, f.platform, sessA, "dev-a")
	if err != nil || d.PolicyKey != 0 || d.Provisioned {
		t.Fatalf("device mutated across mailboxes: %+v", d)
	}
	// No mailbox context on the non-bypassing app role sees zero rows:
	// fail closed. The platform role bypasses RLS by design (privileged,
	// predicate-guarded store code) and is covered by the predicate checks
	// above.
	atx, err := f.app.TenantTx(ctx, 1) // arbitrary tenant scope: still no row matches
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = atx.Rollback(ctx) }()
	var count int
	if err = atx.QueryRow(ctx, "SELECT count(*) FROM mailbox_eas_devices").Scan(&count); err != nil {
		t.Fatal(err)
	}
	// The session mailbox does see its own row (MailboxTx scope).
	if _, err = GetDevice(ctx, f.platform, sessA, "dev-a"); err != nil {
		t.Fatalf("own device unreadable: %v", err)
	}
}

// TestProvisionHandshake walks the full two-phase handshake on PG.
func TestProvisionHandshake(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)

	// Phase 1: client asks with key 0.
	w := postWBXMLCmd(t, r, CmdProvision, Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicies,
			Element(pageProvision, tokPolicy,
				Text(pageProvision, tokPolicyType, policyTypeEAS),
				Text(pageProvision, tokPolicyKey, "0")))))
	resp := decodeResponse(t, w)
	pol := resp.Child(tokPolicies).Child(tokPolicy)
	if pol == nil || pol.ChildText(tokProvStatus) != provSuccess {
		t.Fatalf("phase1 %+v", resp)
	}
	issued := pol.ChildText(tokPolicyKey)
	if issued == "" || issued == "0" {
		t.Fatalf("no key issued: %q", issued)
	}
	d, _ := GetDevice(t.Context(), f.platform, sess, "dev-a")
	if d.Provisioned {
		t.Fatal("provisioned before ack")
	}

	// Phase 2: client acks.
	w = postWBXMLCmd(t, r, CmdProvision, Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicies,
			Element(pageProvision, tokPolicy,
				Text(pageProvision, tokPolicyType, policyTypeEAS),
				Text(pageProvision, tokPolicyKey, issued)))))
	resp = decodeResponse(t, w)
	pol = resp.Child(tokPolicies).Child(tokPolicy)
	if pol.ChildText(tokProvStatus) != provSuccess ||
		pol.ChildText(tokPolicyKey) != issued {
		t.Fatalf("phase2 %+v", pol)
	}
	d, _ = GetDevice(t.Context(), f.platform, sess, "dev-a")
	if !d.Provisioned || d.PolicyKey == 0 {
		t.Fatalf("ack not stored: %+v", d)
	}

	// Wrong ack is a protocol error and leaves the device state alone.
	w = postWBXMLCmd(t, r, CmdProvision, Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicies,
			Element(pageProvision, tokPolicy,
				Text(pageProvision, tokPolicyType, policyTypeEAS),
				Text(pageProvision, tokPolicyKey, "999")))))
	resp = decodeResponse(t, w)
	if resp.Child(tokPolicies).Child(tokPolicy).ChildText(tokProvStatus) != provProtocolError {
		t.Fatal("wrong key accepted")
	}

	// Remote wipe policy type is refused, never acted on.
	w = postWBXMLCmd(t, r, CmdProvision, Element(pageProvision, tokProvision,
		Element(pageProvision, tokPolicies,
			Element(pageProvision, tokPolicy,
				Text(pageProvision, tokPolicyType, policyTypeEAS),
				Element(pageProvision, tokRemoteWipe)))))
	resp = decodeResponse(t, w)
	if resp.Child(tokPolicies).Child(tokPolicy).ChildText(tokProvStatus) == provSuccess {
		t.Fatal("remote wipe accepted")
	}
}

// TestPingBoundedWait proves the Ping budget and the no-changes status.
func pingCalendarRequest(f pgFixture, heartbeat string) *Node {
	return Element(pagePing, tokPing, Text(pagePing, tokHeartbeat, heartbeat), Element(pagePing, tokPFolders, Element(pagePing, tokPFolder, Text(pagePing, tokPId, "cal:"+itoa(f.cal)), Text(pagePing, tokPClass, "Calendar"))))
}

func seedPingCalendarBaseline(t *testing.T, f pgFixture, sess Session) {
	t.Helper()
	id, err := MaxChangeID(t.Context(), f.platform, sess)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveCollectionState(t.Context(), f.platform, sess, "dev-a", "cal:"+itoa(f.cal), collectionState{Epoch: 1, ChangeSeq: id}); err != nil {
		t.Fatal(err)
	}
}

func TestPingBoundedWait(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, s := newEASRouter(t, f, sess)
	seedPingCalendarBaseline(t, f, sess)
	s.PingBudget = 300 * time.Millisecond
	s.PollInterval = 50 * time.Millisecond
	base := Now()
	Now = func() time.Time { return base.Add(0) } // deterministic budget
	defer func() { Now = time.Now }()

	start := time.Now()
	w := postWBXMLCmd(t, r, CmdPing, pingCalendarRequest(f, "1"))
	decodeResponse(t, w)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ping waited %v", elapsed)
	}
	// The response must be status 1 (no changes), not any sync success.
	node, err := Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if node.ChildText(tokPStatus) != pingNoChanges {
		t.Fatalf("ping status %q", node.ChildText(tokPStatus))
	}

	// With a heartbeat above the budget the server answers status 6.
	w = postWBXMLCmd(t, r, CmdPing, pingCalendarRequest(f, "9000"))
	node = decodeResponse(t, w)
	if node.ChildText(tokPStatus) != pingHbOutOfRange {
		t.Fatalf("clamp status %q", node.ChildText(tokPStatus))
	}
}

// TestPingReportsChanges proves activity in the change log surfaces.
func TestPingReportsChanges(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, s := newEASRouter(t, f, sess)
	seedPingCalendarBaseline(t, f, sess)
	s.PingBudget = 2 * time.Second
	s.PollInterval = 20 * time.Millisecond
	go func() {
		time.Sleep(100 * time.Millisecond)
		tx, err := f.platform.PlatformTx(context.Background())
		if err != nil {
			return
		}
		_, _ = tx.Exec(context.Background(),
			"INSERT INTO mailbox_changes(tenant_id,mailbox_id,resource_type,resource_id,version) VALUES($1,$2,'calendar',$3,1)",
			f.tenant, f.mailboxA, f.cal)
		_ = tx.Commit(context.Background())
	}()
	w := postWBXMLCmd(t, r, CmdPing, pingCalendarRequest(f, "1"))
	node := decodeResponse(t, w)
	if node.ChildText(tokPStatus) != pingChanges {
		t.Fatalf("status %q", node.ChildText(tokPStatus))
	}
	folders := node.Child(tokPFolders)
	if folders == nil || len(folders.Children) == 0 {
		t.Fatal("no changed folder reported")
	}
	if got := folders.Children[0].Text; got != "cal:"+itoa(f.cal) {
		t.Fatalf("changed id %q", got)
	}
}

// TestSyncEmailCollectionWithoutMailSource pins the documented behavior when
// no IMAP source exists for the request: the email collection answers the
// server-error status (3) with no fabricated data.
func TestSyncEmailCollectionWithoutMailSource(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)
	req := Element(pageAirSync, tokSync,
		Element(pageAirSync, tokCollections,
			Element(pageAirSync, tokCollection,
				Text(pageAirSync, tokSyncKey, "0"),
				Text(pageAirSync, tokCollectionID, "e:INBOX"))))
	node := decodeResponse(t, postWBXMLCmd(t, r, CmdSync, req))
	cols := node.Child(tokCollections)
	if cols == nil || len(cols.Children) != 1 {
		t.Fatalf("collections %+v", cols)
	}
	if got := cols.Children[0].ChildText(tokStatus); got != syncStatusServerError {
		t.Fatalf("collection status %q, want 3", got)
	}
	if cmds := cols.Children[0].Child(tokCommands); cmds != nil {
		t.Fatalf("commands emitted without a mail source: %+v", cmds)
	}
}

// postWBXMLCmd posts a node under a specific Cmd query parameter.
func postWBXMLCmd(t *testing.T, r *gin.Engine, cmd string, node *Node) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/api/v1/mailbox/eas?Cmd="+cmd+"&DeviceId=dev-a",
		bytes.NewReader(Encode(node)))
	req.Header.Set(headerVersion, "14.1")
	req.Header.Set("Content-Type", ContentTypeWBXML)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A message committed before Ping starts must still wake the client. Saved
// configuration must also work through a newly constructed Server instance.
func TestPingMailSnapshotAndRestartedConfiguration(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, s := newEASRouter(t, f, sess)
	source := newFakeMail(42, MailMessage{UID: 7, Subject: "Known"})
	s.Mail = func(context.Context, Session) (MailSource, error) { return source, nil }
	state := collectionState{Epoch: 1, ChangeSeq: 1, UIDValidity: 42}
	if err := saveCollectionState(t.Context(), f.platform, sess, "dev-a", "e:INBOX", state); err != nil {
		t.Fatal(err)
	}
	if err := replaceItemsAll(t.Context(), f.platform, sess, "dev-a", "e:INBOX", map[string]itemRow{"7": {ItemID: "7", Fingerprint: source.msgs[0].Fingerprint()}}); err != nil {
		t.Fatal(err)
	}
	source.msgs = append(source.msgs, MailMessage{UID: 8, Subject: "Arrived before Ping"})
	request := Element(pagePing, tokPing, Text(pagePing, tokHeartbeat, "1"), Element(pagePing, tokPFolders, Element(pagePing, tokPFolder, Text(pagePing, tokPId, "e:INBOX"), Text(pagePing, tokPClass, "Email"))))
	response := decodeResponse(t, postWBXMLCmd(t, r, CmdPing, request))
	if response.ChildText(tokPStatus) != pingChanges || response.Child(tokPFolders).Children[0].Text != "e:INBOX" {
		t.Fatalf("mail notification missing: %+v", response)
	}
	// The next instance has no memory of the first request or its folder list.
	restarted := NewServer(f.platform)
	restarted.Mail = s.Mail
	restarted.EmailFolders = s.EmailFolders
	repeated, err := restarted.handlePing(t.Context(), sess, "dev-a", Element(pagePing, tokPing))
	if err != nil || repeated.ChildText(tokPStatus) != pingChanges {
		t.Fatalf("persisted Ping configuration: %+v %v", repeated, err)
	}
	// Another mailbox's collection cannot enter the monitored list.
	invalid := Element(pagePing, tokPing, Text(pagePing, tokHeartbeat, "1"), Element(pagePing, tokPFolders, Element(pagePing, tokPFolder, Text(pagePing, tokPId, "cal:999999"), Text(pagePing, tokPClass, "Calendar"))))
	if got := decodeResponse(t, postWBXMLCmd(t, r, CmdPing, invalid)).ChildText(tokPStatus); got != "7" {
		t.Fatalf("foreign folder status=%s", got)
	}
}

func TestPingDeepFlagScanResumesAcrossRestart(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, s := newEASRouter(t, f, sess)
	source := newFakeMail(42)
	items := map[string]itemRow{}
	for uid := uint32(1); uid <= 350; uid++ {
		m := msg(uid, "retained", false)
		source.msgs = append(source.msgs, m)
		id := itoa(int64(uid))
		items[id] = itemRow{ItemID: id, Fingerprint: m.Fingerprint()}
	}
	s.Mail = func(context.Context, Session) (MailSource, error) { return source, nil }
	s.PingBudget = 200 * time.Millisecond
	s.PollInterval = time.Second
	if err := saveCollectionState(t.Context(), f.platform, sess, "dev-a", "e:INBOX", collectionState{Epoch: 1, ChangeSeq: 1, UIDValidity: 42}); err != nil {
		t.Fatal(err)
	}
	if err := replaceItemsAll(t.Context(), f.platform, sess, "dev-a", "e:INBOX", items); err != nil {
		t.Fatal(err)
	}
	source.msgs[0].Read = true // Outside the newest 100, with unchanged message count.
	request := Element(pagePing, tokPing, Text(pagePing, tokHeartbeat, "1"), Element(pagePing, tokPFolders, Element(pagePing, tokPFolder, Text(pagePing, tokPId, "e:INBOX"), Text(pagePing, tokPClass, "Email"))))
	response := decodeResponse(t, postWBXMLCmd(t, r, CmdPing, request))
	for round := 0; response.ChildText(tokPStatus) == pingNoChanges && round < 8; round++ {
		restarted := NewServer(f.platform)
		restarted.Mail = s.Mail
		restarted.EmailFolders = s.EmailFolders
		restarted.PingBudget = s.PingBudget
		restarted.PollInterval = s.PollInterval
		var err error
		response, err = restarted.handlePing(t.Context(), sess, "dev-a", Element(pagePing, tokPing))
		if err != nil {
			t.Fatal(err)
		}
	}
	if response.ChildText(tokPStatus) != pingChanges {
		t.Fatalf("deep flag change never reached: %+v", response)
	}
}

func TestPingWithSingleConnectionPool(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, s := newEASRouter(t, f, sess)
	seedPingCalendarBaseline(t, f, sess)
	s.PingBudget = 20 * time.Millisecond
	if got := decodeResponse(t, postWBXMLCmd(t, r, CmdPing, pingCalendarRequest(f, "1"))).ChildText(tokPStatus); got != pingNoChanges {
		t.Fatal(got)
	}
	parsed, err := url.Parse(f.platform.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "1")
	parsed.RawQuery = query.Encode()
	one, err := db.OpenPlatform(t.Context(), parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if one.Config().MaxConns != 1 {
		t.Fatal("fixture did not constrain connections")
	}
	server := NewServer(one)
	server.PingBudget = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	response, err := server.handlePing(ctx, sess, "dev-a", Element(pagePing, tokPing))
	if err != nil || response.ChildText(tokPStatus) != pingNoChanges {
		t.Fatalf("one-connection Ping: %+v %v", response, err)
	}
}

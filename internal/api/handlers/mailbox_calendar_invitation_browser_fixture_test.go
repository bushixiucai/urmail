package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestCalendarInvitationBrowserFixturePG retains actual authentication, DKIM,
// HTTP and PG services; IMAP transport, DNS TXT and SMTP are explicit isolated
// fixtures. The signatures themselves use a separately generated real key.
func TestCalendarInvitationBrowserFixturePG(t *testing.T) {
	dir := os.Getenv("URMAIL_INVITATION_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("browser fixture not requested")
	}
	f := newSchedulingHTTPFixture(t)
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Second)
	stamp := time.Now().UTC().Truncate(time.Second)
	invite := collab.Invitation{UID: "browser-invitation@outside.example", Method: collab.MethodRequest, Organizer: "host@outside.example", Stamp: stamp, Event: collab.Event{Summary: "Verified external browser meeting", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: "Asia/Shanghai", RRule: "FREQ=WEEKLY;COUNT=3", Attendees: []string{f.base.principal.Address}}}
	for _, uid := range []uint32{10, 11, 12, 13, 14} {
		current := invite
		title := current.Event.Summary
		switch uid {
		case 11:
			current.UID = "unverified-browser@outside.example"
			current.Event.Summary = "Unverified browser copy"
			title = current.Event.Summary
		case 12:
			current.Sequence = 1
			current.Stamp = stamp.Add(time.Second)
			current.Event.StartsAt = start.Add(time.Hour)
			current.Event.EndsAt = start.Add(2 * time.Hour)
			title = "Updated external meeting"
		case 13:
			current.Sequence = 2
			current.Stamp = stamp.Add(2 * time.Second)
			current.Method = collab.MethodCancel
			current.Event.Status = "cancelled"   //nolint:misspell // RFC 5545 value.
			title = "Cancelled external meeting" //nolint:misspell // Matches the established cancellation mail subject.
		case 14:
			current.UID = "unknown-browser@outside.example"
			current.Event.Summary = "Response delivery unknown"
			title = current.Event.Summary
		} //nolint:misspell // RFC 5545 value.
		body, err := collab.InvitationICS(current)
		if err != nil {
			t.Fatal(err)
		}
		raw := f.sign(t, current.Organizer, f.base.principal.Address, body, int(uid))
		if uid == 11 {
			raw = []byte(strings.Replace(string(raw), "SUMMARY:Unverified browser copy", "SUMMARY:Unverified browser copy (modified)", 1))
		}
		f.mail.raw[uid] = raw
		f.base.mail.messages["INBOX"][uid] = &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid, Subject: title, From: []webmail.Address{{Email: current.Organizer}}, Date: fixtureNow(), Size: int64(len(raw))}, UIDValidity: 9, To: []webmail.Address{{Email: f.base.principal.Address}}, Text: "Synthetic scheduling mail for isolated browser acceptance.", Attachments: []webmail.AttachmentMeta{{PartID: "1", Filename: "invitation.ics", ContentType: "text/calendar", Size: int64(len(body))}}}
	}
	group := f.base.router.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(StoreMailboxNative{Q: store.New(f.base.pool)}, f.base.native.Vault))
	RegisterCalendarIncomingRoutes(group, f.handler)
	group.GET("/calendars/events", f.handler.ListEvents)
	f.base.router.POST("/__fixture/response-unknown", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+f.token {
			c.AbortWithStatus(403)
			return
		}
		f.submit.mu.Lock()
		f.submit.err = webmail.ErrSubmitUncertain
		f.submit.mu.Unlock()
		c.JSON(200, gin.H{"configured": "unknown"})
	})
	f.base.router.GET("/__fixture/submissions", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+f.token {
			c.AbortWithStatus(403)
			return
		}
		f.submit.mu.Lock()
		defer f.submit.mu.Unlock()
		c.JSON(200, gin.H{"successful_submissions": len(f.submit.calls)})
	})
	RegisterCalendarReminderRoutes(group, f.handler)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	f.base.router.GET("/api/v1/i18n/:locale", func(c *gin.Context) {
		locale := c.Param("locale")
		if locale != "en" && locale != "zh-CN" {
			c.Status(404)
			return
		}
		c.File(filepath.Join(root, "internal/i18n/locales", locale+".json"))
	})
	f.base.router.GET("/api/v1/branding", func(c *gin.Context) { c.JSON(200, gin.H{"name": "urMail", "primary_color": "#2563eb"}) })
	server := httptest.NewServer(f.base.router)
	defer server.Close()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"url": server.URL, "token": f.token, "address": f.base.principal.Address, "folder_enc": webmail.EncodeFolderName("INBOX"), "uid_validity": 9, "source_uids": []int{10, 11, 12, 13, 14}, "transport": "explicit IMAP/SMTP/DNS TXT fixtures, real independently keyed DKIM and PostgreSQL HTTP"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(20 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-timer.C:
			t.Log("invitation browser fixture closed")
			return
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(dir, "stop")); e == nil {
				return
			}
		}
	}
}

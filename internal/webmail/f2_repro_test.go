package webmail

import (
	"testing"
	"time"
)

// QA regression F-2 repro: three same-subject bounces with the seeded
// sender variants must stay three separate threads.
func TestF2BounceVariantsStaySeparate(t *testing.T) {
	bounce := func(uid uint32, from string) MessageSummary {
		return MessageSummary{
			UID: uid, Subject: "Undelivered Mail Returned to Sender",
			From: []Address{{Name: "Mail Delivery System", Email: from}},
			Date: envelopeDate(time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)),
		}
	}
	msgs := []MessageSummary{
		bounce(1, "MAILER-DAEMON@example.test"),
		bounce(2, "MAILER-DAEMON@box.example.test"),
		bounce(3, "postmaster@example.test"),
	}
	threads := BuildThreads(msgs)
	for _, m := range msgs {
		if threads[m.UID] != m.UID {
			t.Fatalf("bounce uid %d grouped into %d (want itself)", m.UID, threads[m.UID])
		}
	}
	// And the host-NIL ENVELOPE shape (display-only address, empty email):
	msgs2 := []MessageSummary{
		{UID: 1, Subject: "Undelivered Mail Returned to Sender", From: []Address{{Name: "MAILER-DAEMON@example.test", Email: ""}}, Date: envelopeDate(time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC))},
		{UID: 2, Subject: "Undelivered Mail Returned to Sender", From: []Address{{Name: "MAILER-DAEMON@box.example.test", Email: ""}}, Date: envelopeDate(time.Date(2026, 11, 10, 4, 30, 0, 0, time.UTC))},
	}
	t2 := BuildThreads(msgs2)
	t.Logf("host-nil emails: threads=%v", t2)
}

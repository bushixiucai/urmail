package webmail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMailboxWireRoundTrip(t *testing.T) {
	for _, name := range []string{"INBOX", "Projects/项目 & 资料", "工作📬", "A&B"} {
		encoded := encodeMailboxWire(name)
		if decodeMailboxWire(encoded) != name {
			t.Fatalf("wire round trip %q: %q", name, encoded)
		}
		if strings.Contains(encoded, "项目") {
			t.Fatal("non-ASCII mailbox sent without modified UTF-7")
		}
	}
	if encodeMailboxWire("台北") != "&U,BTFw-" {
		t.Fatal("RFC modified UTF-7 example mismatch")
	}
	for _, value := range []string{"broken&name", "&!x-", "&AA-"} {
		if decodeMailboxWire(value) != "" {
			t.Fatal("malformed wire mailbox accepted")
		}
	}
	for _, value := range []string{"", " x", "a\r\nDELETE INBOX", "a//b", "../x"} {
		if ValidManagedFolder(value) {
			t.Fatalf("unsafe name %q", value)
		}
	}
	if !ProtectedFolder(Folder{Name: "localized", Flags: []string{`\Trash`}}) {
		t.Fatal("localized special-use mailbox unprotected")
	}
}

func TestTransferRequiresUIDPlusEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, ack string
		closed    bool
		want      []string
	}{
		{"partial", "* OK [COPYUID 75 2,4 11:12] copied\r\n", false, []string{"success", "failed", "success"}},
		{"without_uidplus", "", false, []string{"unknown", "unknown", "unknown"}},
		{"lost_reply", "", true, []string{"unknown", "unknown", "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			go func() {
				defer func() { _ = server.Close() }()
				reader := bufio.NewReader(server)
				line, _ := reader.ReadString('\n')
				tag := strings.Fields(line)[0]
				_, _ = fmt.Fprintf(server, "* OK [UIDVALIDITY 8] valid\r\n%s OK selected\r\n", tag)
				line, _ = reader.ReadString('\n')
				if tc.closed {
					return
				}
				fields := strings.Fields(line)
				_, _ = fmt.Fprintf(server, "%s%s OK copied\r\n", tc.ack, fields[0])
			}()
			c := &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second}
			result, err := c.TransferMessages(context.Background(), "INBOX", []uint32{2, 3, 4}, "Target", false)
			if tc.closed && err == nil {
				t.Fatal("lost server reply accepted")
			}
			if !tc.closed && err != nil {
				t.Fatal(err)
			}
			for i, item := range result.Results {
				if item.Status != tc.want[i] {
					t.Fatalf("outcomes %+v", result)
				}
			}
			if tc.name == "partial" && (result.UIDValidity != 75 || result.Results[2].DestinationUID != 12) {
				t.Fatal("destination binding lost")
			}
		})
	}
}

func TestUIDPlusSetBounds(t *testing.T) {
	for _, value := range []string{"0", "1:1000000000", "4294967296", "*", "4:1", "1:2:3"} {
		if _, err := expandUIDPlusSet(value); err == nil {
			t.Fatalf("unbounded UIDPLUS set %s", value)
		}
	}
}

func TestFolderSubscriptionFailureDoesNotUndoNamespaceSuccess(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprint(rename), func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			go func() {
				defer func() { _ = server.Close() }()
				reader := bufio.NewReader(server)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					fields := strings.Fields(line)
					status := "OK changed"
					if fields[1] == "SUBSCRIBE" {
						status = "NO subscription unavailable"
					}
					_, _ = fmt.Fprintf(server, "%s %s\r\n", fields[0], status)
				}
			}()
			c := &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second}
			var err error
			if rename {
				err = c.RenameFolder(context.Background(), "Old", "New")
			} else {
				err = c.CreateFolder(context.Background(), "New")
			}
			if err != nil {
				t.Fatalf("authoritative success misreported: %v", err)
			}
		})
	}
}

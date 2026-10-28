package webmail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func importWirePeer(t *testing.T, response func(string, int) string) *imapConn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		for n := 0; ; n++ {
			line, e := reader.ReadString('\n')
			if e != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return
			}
			body := response(strings.TrimSpace(line), n)
			body = strings.ReplaceAll(body, "$TAG", fields[0])
			if _, e = io.WriteString(server, body); e != nil {
				return
			}
		}
	}()
	return &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second, validities: map[string]uint32{}}
}

func TestImportInventoryRequiresCompleteSingleBoundedSearch(t *testing.T) {
	var over strings.Builder
	over.WriteString("* SEARCH")
	for n := 1; n <= 101; n++ {
		fmt.Fprintf(&over, " %d", n)
	}
	over.WriteString("\r\n$TAG OK done\r\n")
	tests := []struct {
		name, reply string
		valid       bool
	}{
		{"complete_empty", "* SEARCH\r\n$TAG OK done\r\n", true},
		{"complete_candidates", "* SEARCH 1 3\r\n$TAG OK done\r\n", true},
		{"missing_search", "$TAG OK done\r\n", false},
		{"multiple_search", "* SEARCH 1\r\n* SEARCH 2\r\n$TAG OK done\r\n", false},
		{"partial_esearch", "* ESEARCH UID ALL 1:3\r\n$TAG OK done\r\n", false},
		{"duplicate_uid", "* SEARCH 1 1\r\n$TAG OK done\r\n", false},
		{"zero_uid", "* SEARCH 0\r\n$TAG OK done\r\n", false},
		{"overflow_uid", "* SEARCH 4294967296\r\n$TAG OK done\r\n", false},
		{"candidate_budget", over.String(), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := importWirePeer(t, func(string, int) string { return tc.reply })
			_, e := c.importUIDSearch(t.Context(), "ALL")
			if (e == nil) != tc.valid {
				t.Fatalf("complete inventory=%v error=%v", tc.valid, e)
			}
		})
	}
}

func TestImportInventoryRejectsUIDWatermarkAndGenerationRaces(t *testing.T) {
	for _, kind := range []string{"outside_watermark", "uidnext_changed", "validity_changed", "status_missing", "status_duplicate", "complete_absence"} {
		t.Run(kind, func(t *testing.T) {
			statuses := 0
			c := importWirePeer(t, func(line string, _ int) string {
				if strings.Contains(line, " SELECT ") {
					return "* OK [UIDVALIDITY 9] pinned\r\n$TAG OK selected\r\n"
				}
				if strings.Contains(line, " STATUS ") {
					statuses++
					if kind == "status_missing" {
						return "$TAG OK done\r\n"
					}
					if kind == "status_duplicate" {
						return "* STATUS INBOX (UIDVALIDITY 9 UIDNEXT 5)\r\n* STATUS INBOX (UIDVALIDITY 9 UIDNEXT 5)\r\n$TAG OK done\r\n"
					}
					v, next := 9, 5
					if statuses > 1 && kind == "uidnext_changed" {
						next = 6
					}
					if statuses > 1 && kind == "validity_changed" {
						v = 10
					}
					return "* STATUS INBOX (UIDVALIDITY " + strconv.Itoa(v) + " UIDNEXT " + strconv.Itoa(next) + ")\r\n$TAG OK done\r\n"
				}
				if strings.Contains(line, " UID SEARCH ") {
					if kind == "outside_watermark" {
						return "* SEARCH 5\r\n$TAG OK done\r\n"
					}
					return "* SEARCH\r\n$TAG OK done\r\n"
				}
				return "$TAG BAD unexpected\r\n"
			})
			raw := []byte("Message-ID: <wire@fixture.test>\r\nSubject: wire\r\n\r\nOriginal\r\n")
			matches, e := c.FindImportCopies(context.Background(), "INBOX", raw, 9)
			if kind == "complete_absence" {
				if e != nil || len(matches) != 0 {
					t.Fatal(matches, e)
				}
			} else if e == nil {
				t.Fatal("unsafe absence accepted", kind, matches)
			}
		})
	}
}

func TestImportInventoryRefusesLiteralBeforeBudgetAllocation(t *testing.T) {
	raw := []byte("Message-ID: <budget@fixture.test>\r\nSubject: fixture\r\n\r\nBody\r\n")
	c := importWirePeer(t, func(line string, _ int) string {
		switch {
		case strings.Contains(line, " SELECT "):
			return "* OK [UIDVALIDITY 9] pinned\r\n$TAG OK selected\r\n"
		case strings.Contains(line, " STATUS "):
			return "* STATUS INBOX (UIDVALIDITY 9 UIDNEXT 5)\r\n$TAG OK status\r\n"
		case strings.Contains(line, " UID SEARCH "):
			return "* SEARCH 1\r\n$TAG OK search\r\n"
		case strings.Contains(line, " UID FETCH "):
			return "* 1 FETCH (UID 1 BODY[] {1000000}\r\n"
		default:
			return "$TAG BAD unexpected\r\n"
		}
	})
	_, e := c.FindImportCopies(WithImportReadBudget(t.Context(), 8), "INBOX", raw, 9)
	if e == nil || !strings.Contains(e.Error(), "budget") {
		t.Fatal("unbounded FETCH or unsafe absence", e)
	}
}

package webmail

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// startUIDPlusMoveServer answers every command with a bare tagged OK, except
// UID MOVE, which replies with the given tagged OK suffix carrying UIDPLUS
// response codes, e.g. "[COPYUID 42 5 11] Move completed".
func startUIDPlusMoveServer(t *testing.T, moveSuffix string) (addr string, cmds chan string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cmds = make(chan string, 8)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(w, "* OK ready\r\n")
		_ = w.Flush()
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			tag := fields[0]
			cmds <- strings.TrimSpace(line)
			if strings.Contains(line, "UID MOVE") {
				if strings.HasPrefix(moveSuffix, "* ") {
					switch {
					case strings.Contains(moveSuffix, "__TAGGED__"):
						parts := strings.SplitN(moveSuffix, "__TAGGED__", 2)
						_, _ = io.WriteString(w, parts[0]+"\r\n"+tag+" OK "+parts[1]+"\r\n")
					case strings.HasSuffix(moveSuffix, "__REJECT__"):
						_, _ = io.WriteString(w, strings.TrimSuffix(moveSuffix, "__REJECT__")+"\r\n"+tag+" NO Move rejected\r\n")
					case strings.HasSuffix(moveSuffix, "__OKAY__"):
						_, _ = io.WriteString(w, strings.TrimSuffix(moveSuffix, "__OKAY__")+"\r\n"+tag+" OKAY Invalid status\r\n")
					default:
						_, _ = io.WriteString(w, moveSuffix+"\r\n"+tag+" OK Move completed\r\n")
					}
				} else {
					_, _ = io.WriteString(w, tag+" OK "+moveSuffix+"\r\n")
				}
			} else {
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()
	return ln.Addr().String(), cmds
}

func dialTestClient(t *testing.T, addr string) Client {
	t.Helper()
	client, err := (NetDialer{Addr: addr, Timeout: 2 * time.Second}).Dial(context.Background(), "u@e.test", "pw")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestMoveMessagesCOPYUIDSingleAndRange(t *testing.T) {
	addr, cmds := startUIDPlusMoveServer(t, "[COPYUID 42 5,7:9 11,13:15] Move completed")
	client := dialTestClient(t, addr)
	destinations, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5, 7, 8, 9}, "Archive")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	want := []uint32{11, 13, 14, 15}
	if len(destinations.DestinationUIDs) != len(want) {
		t.Fatalf("destinations=%v want=%v", destinations, want)
	}
	for i := range want {
		if destinations.DestinationUIDs[i] != want[i] {
			t.Fatalf("destinations=%v want=%v", destinations, want)
		}
	}
	// Commands arrive in order: LOGIN, SELECT, UID MOVE. Drain until the move.
	var moveCmd string
	deadline := time.After(2 * time.Second)
	for moveCmd == "" {
		select {
		case cmd := <-cmds:
			if strings.Contains(cmd, "UID MOVE") {
				moveCmd = cmd
			}
		case <-deadline:
			t.Fatal("timed out waiting for UID MOVE command")
		}
	}
	if !strings.Contains(moveCmd, "UID MOVE 5,7,8,9") || !strings.Contains(moveCmd, `"Archive"`) {
		t.Fatalf("move command %q", moveCmd)
	}
}

func TestMoveMessagesUntaggedCOPYUID(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "* OK [COPYUID 42 5,7:8 11,13:14] Moved")
	client := dialTestClient(t, addr)
	destinations, err := client.MoveMessages(context.Background(), "Sent", []uint32{8, 5, 7}, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations.DestinationUIDs) != 3 || destinations.DestinationUIDValidity != 42 || destinations.DestinationUIDs[0] != 14 || destinations.DestinationUIDs[1] != 11 || destinations.DestinationUIDs[2] != 13 {
		t.Fatalf("destinations=%v want=[14 11 13]", destinations)
	}
}

func TestMoveMessagesConflictingCOPYUIDDoesNotOfferUndo(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "* OK [COPYUID 42 5 11] Moved\r\n* OK [COPYUID 42 5 12] Moved")
	client := dialTestClient(t, addr)
	destinations, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
	if err != nil || destinations.DestinationUIDs != nil || destinations.DestinationUIDValidity != 0 {
		t.Fatalf("conflicting mapping must not offer undo: destinations=%v error=%v", destinations, err)
	}
}

func TestMoveMessagesMalformedConflictingCOPYUIDOmitsUndo(t *testing.T) {
	for _, response := range []string{
		"* OK [COPYUID 42 5 11] Moved__TAGGED__[COPYUID\t42 5 12] Moved",
		"* OK [COPYUID\t42 5 12] Moved__TAGGED__[COPYUID 42 5 11] Moved",
		"* OK [COPYUID] Moved__TAGGED__[COPYUID 42 5 11] Moved",
		"* OK [COPYUID 42 5 11] Moved__TAGGED__[COPYUID(42 5 12)] Moved",
		"* OK [COPYUID(42 5 12)] Moved__TAGGED__[COPYUID 42 5 11] Moved",
	} {
		t.Run(response, func(t *testing.T) {
			addr, _ := startUIDPlusMoveServer(t, response)
			client := dialTestClient(t, addr)
			result, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
			if err != nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
				t.Fatalf("malformed mapping must omit undo: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestMoveMessagesUnrelatedResponseDoesNotSuppressCOPYUID(t *testing.T) {
	for _, tagged := range []string{"Notice [COPYUID\t42 5 12]", "[COPYUIDX 42 5 12] Notice"} {
		t.Run(tagged, func(t *testing.T) {
			addr, _ := startUIDPlusMoveServer(t, "* OK [COPYUID 42 5 11] Moved__TAGGED__"+tagged)
			client := dialTestClient(t, addr)
			result, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
			if err != nil || result.DestinationUIDValidity != 42 || len(result.DestinationUIDs) != 1 || result.DestinationUIDs[0] != 11 {
				t.Fatalf("unrelated text suppressed mapping: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestMoveMessagesUnusableCOPYUIDOmitsUndo(t *testing.T) {
	for _, response := range []string{
		"* OK Notice: [COPYUID 42 5 99] is only text",
		"Notice: [COPYUID 42 5 99] is only text",
		"* OK [COPYUID 0 5 11] Moved",
		"* OK [COPYUID 42 5,5 11,12] Moved",
		"* OK [COPYUID 42 5,6 11,11] Moved",
		"* OK [COPYUID 42 5 11,12] Moved",
	} {
		t.Run(response, func(t *testing.T) {
			addr, _ := startUIDPlusMoveServer(t, response)
			client := dialTestClient(t, addr)
			requested := []uint32{5}
			if strings.Contains(response, "5,5") || strings.Contains(response, "5,6") {
				requested = []uint32{5, 6}
			}
			result, err := client.MoveMessages(context.Background(), "Sent", requested, "Trash")
			if err != nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
				t.Fatalf("unusable mapping must omit undo: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestMoveMessagesTaggedRejectionDiscardsCOPYUID(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "* OK [COPYUID 42 5 11] Moved__REJECT__")
	client := dialTestClient(t, addr)
	result, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
	if err == nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
		t.Fatalf("rejected move must not expose mapping: result=%+v err=%v", result, err)
	}
}

func TestMoveMessagesCaseInsensitiveCOPYUID(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "* ok [CopyUid 42 5 11] Moved")
	client := dialTestClient(t, addr)
	result, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
	if err != nil || result.DestinationUIDValidity != 42 || len(result.DestinationUIDs) != 1 || result.DestinationUIDs[0] != 11 {
		t.Fatalf("case-insensitive response: result=%+v err=%v", result, err)
	}
}

func TestMoveMessagesMalformedTaggedStatusDiscardsCOPYUID(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "* OK [COPYUID 42 5 11] Moved__OKAY__")
	client := dialTestClient(t, addr)
	result, err := client.MoveMessages(context.Background(), "Sent", []uint32{5}, "Trash")
	if err == nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
		t.Fatalf("invalid status must not expose mapping: result=%+v err=%v", result, err)
	}
}

func TestMoveMessagesCOPYUIDUnorderedSourceSet(t *testing.T) {
	// Servers may list the source set unsorted; the mapping is positional, so
	// destinations must still align with the requested uid order.
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 9,5 15,11] Move completed")
	client := dialTestClient(t, addr)
	destinations, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5, 9}, "Archive")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(destinations.DestinationUIDs) != 2 || destinations.DestinationUIDs[0] != 11 || destinations.DestinationUIDs[1] != 15 {
		t.Fatalf("destinations=%v want=[11 15]", destinations)
	}
}

func TestMoveMessagesCOPYUIDMissingUIDRejected(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 5 11] Move completed")
	client := dialTestClient(t, addr)
	result, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5, 6}, "Archive")
	if err != nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
		t.Fatalf("partial mapping must omit undo: result=%+v err=%v", result, err)
	}
}

func TestMoveMessagesCOPYUIDMalformedRejected(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 5 11,12] Move completed")
	client := dialTestClient(t, addr)
	result, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5}, "Archive")
	if err != nil || result.DestinationUIDs != nil || result.DestinationUIDValidity != 0 {
		t.Fatalf("malformed mapping must omit undo: result=%+v err=%v", result, err)
	}
}

func TestMoveMessagesInvalidArgs(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "Move completed")
	client := dialTestClient(t, addr)
	if _, err := client.MoveMessages(context.Background(), "INBOX", nil, "Archive"); err == nil {
		t.Fatal("expected error for empty uids")
	}
	if _, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5}, ""); err == nil {
		t.Fatal("expected error for empty destination")
	}
}

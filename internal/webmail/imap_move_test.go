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
				_, _ = io.WriteString(w, tag+" OK "+moveSuffix+"\r\n")
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
	if len(destinations) != len(want) {
		t.Fatalf("destinations=%v want=%v", destinations, want)
	}
	for i := range want {
		if destinations[i] != want[i] {
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

func TestMoveMessagesCOPYUIDUnorderedSourceSet(t *testing.T) {
	// Servers may list the source set unsorted; the mapping is positional, so
	// destinations must still align with the requested uid order.
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 9,5 15,11] Move completed")
	client := dialTestClient(t, addr)
	destinations, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5, 9}, "Archive")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(destinations) != 2 || destinations[0] != 11 || destinations[1] != 15 {
		t.Fatalf("destinations=%v want=[11 15]", destinations)
	}
}

func TestMoveMessagesCOPYUIDMissingUIDRejected(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 5 11] Move completed")
	client := dialTestClient(t, addr)
	if _, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5, 6}, "Archive"); err == nil {
		t.Fatal("expected error when COPYUID omits a requested uid")
	}
}

func TestMoveMessagesCOPYUIDMalformedRejected(t *testing.T) {
	addr, _ := startUIDPlusMoveServer(t, "[COPYUID 42 5 11,12] Move completed")
	client := dialTestClient(t, addr)
	if _, err := client.MoveMessages(context.Background(), "INBOX", []uint32{5}, "Archive"); err == nil {
		t.Fatal("expected error on length-mismatched COPYUID")
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

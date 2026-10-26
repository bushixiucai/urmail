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

func TestNetSubmitterSend(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		write := func(s string) {
			_, _ = io.WriteString(w, s+"\r\n")
			_ = w.Flush()
		}
		write("220 localhost ESMTP")
		line, _ := r.ReadString('\n') // EHLO
		_ = line
		write("250-localhost")
		write("250 AUTH PLAIN LOGIN")
		line, _ = r.ReadString('\n') // AUTH
		if !strings.HasPrefix(strings.ToUpper(line), "AUTH") {
			got <- "missing auth: " + line
			return
		}
		write("235 OK")
		_, _ = r.ReadString('\n') // MAIL
		write("250 OK")
		_, _ = r.ReadString('\n') // RCPT
		write("250 OK")
		_, _ = r.ReadString('\n') // DATA
		write("354 Go")
		var payload strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			if line == ".\r\n" {
				break
			}
			payload.WriteString(line)
		}
		write("250 Queued")
		_, _ = r.ReadString('\n') // QUIT
		write("221 Bye")
		got <- payload.String()
	}()

	sub := NetSubmitter{Addr: ln.Addr().String(), Timeout: 2 * time.Second}
	err = sub.Send(context.Background(), "alice@example.test", "secret", Outgoing{
		To:      []string{"bob@example.test"},
		Subject: "Hi",
		Text:    "Hello bob",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case payload := <-got:
		if !strings.Contains(payload, "Subject: Hi") || !strings.Contains(payload, "Hello bob") {
			t.Fatalf("payload=%q", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

package webmail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPSubmissionCommitBoundary(t *testing.T) {
	for _, mode := range []string{"quit_disconnect", "reply_lost", "data_rejected"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan bool, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					accepted <- false
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				r := bufio.NewReader(conn)
				write := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
				write("220 localhost")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						accepted <- false
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						write("250-localhost")
						write("250 AUTH PLAIN")
					case strings.HasPrefix(line, "AUTH"):
						write("235 OK")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						write("250 OK")
					case strings.HasPrefix(line, "DATA"):
						write("354 continue")
						for {
							line, err = r.ReadString('\n')
							if err != nil {
								accepted <- false
								return
							}
							if line == ".\r\n" {
								break
							}
						}
						accepted <- mode != "data_rejected"
						if mode == "reply_lost" {
							return
						}
						if mode == "data_rejected" {
							write("451 temporary rejection")
							return
						}
						write("250 queued")
						_, _ = r.ReadString('\n') // Deliberately disconnect without a QUIT reply.
						return
					}
				}
			}()
			_, err = (NetSubmitter{Addr: listener.Addr().String(), Timeout: time.Second}).Send(context.Background(), "alice@example.test", "secret", Outgoing{To: []string{"bob@example.test"}, Text: "message"})
			switch mode {
			case "quit_disconnect":
				if err != nil {
					t.Fatalf("committed submission returned failure: %v", err)
				}
			case "reply_lost":
				if !errors.Is(err, ErrSubmitUncertain) {
					t.Fatalf("want uncertain, got %v", err)
				}
			case "data_rejected":
				if !errors.Is(err, ErrSubmitFailed) || errors.Is(err, ErrSubmitUncertain) {
					t.Fatalf("want safe rejection, got %v", err)
				}
			}
			if got := <-accepted; got != (mode != "data_rejected") {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
}

func TestIMAPBlockedCommandHonorsCancellation(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &imapConn{conn: client, r: bufio.NewReader(client)}
	defer c.Close()
	read := make(chan struct{})
	go func() { _, _ = bufio.NewReader(server).ReadString('\n'); close(read) }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.command(ctx, "NOOP") }()
	<-read
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled blocked command succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked command ignored cancellation")
	}
}

func TestFolderUIDValidityRejectsRecreatedMailbox(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second}
	defer c.Close()
	go func() {
		r := bufio.NewReader(server)
		for _, validity := range []uint32{7, 8} {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag := strings.Fields(line)[0]
			_, _ = fmt.Fprintf(server, "* OK [UIDVALIDITY %d] identity\r\n%s OK selected\r\n", validity, tag)
		}
	}()
	if validity, err := c.FolderUIDValidity(context.Background(), "Drafts"); err != nil || validity != 7 {
		t.Fatalf("validity=%d err=%v", validity, err)
	}
	if err := c.selectFolder(context.Background(), "Drafts"); !errors.Is(err, ErrDraftChanged) {
		t.Fatalf("want changed, got %v", err)
	}
}

func TestAppendWithoutUIDProofIsUncertain(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second}
	defer c.Close()
	go func() {
		r := bufio.NewReader(server)
		line, _ := r.ReadString('\n')
		tag := strings.Fields(line)[0]
		_, _ = io.WriteString(server, "+ literal\r\n")
		raw := make([]byte, len("payload"))
		_, _ = io.ReadFull(r, raw)
		end := make([]byte, 2)
		_, _ = io.ReadFull(r, end)
		_, _ = io.WriteString(server, tag+" OK appended\r\n")
	}()
	_, _, err := c.appendLiteral(context.Background(), "Drafts", nil, []byte("payload"))
	if !errors.Is(err, ErrDraftUncertain) {
		t.Fatalf("missing APPENDUID accepted: %v", err)
	}
}

func TestNestedDraftMIMERoundTrip(t *testing.T) {
	payload := []byte{0, 1, 2, 255, 128}
	raw := BuildDraftMIME("alice@example.test", Outgoing{To: []string{"bob@example.test"}, Bcc: []string{"private@example.test"}, Text: "plain", HTML: "<p>html</p>", Attachments: []OutgoingAttachment{{Filename: "binary.bin", ContentType: "application/octet-stream", Content: payload}}})
	text, html, atts, err := parseMIMEBodyStrict([]byte(raw))
	if err != nil || strings.TrimSpace(text) != "plain" || strings.TrimSpace(html) != "<p>html</p>" || len(atts) != 1 {
		t.Fatalf("roundtrip text=%q html=%q atts=%v err=%v", text, html, atts, err)
	}
	_, _, download, err := extractMIMEPart([]byte(raw), atts[0].PartID)
	if err != nil || !bytes.Equal(download, payload) {
		t.Fatalf("attachment mismatch: %v", err)
	}
}

func TestAttachmentDownloadKeepsDecodedBytes(t *testing.T) {
	for _, size := range []int{6 << 20, 8 << 20, 10 << 20, MaxAttachmentBytes + 1} {
		for _, encoding := range []string{"binary", "base64"} {
			t.Run(fmt.Sprintf("%d_%s", size, encoding), func(t *testing.T) {
				payload := bytes.Repeat([]byte{0x31}, size)
				var raw bytes.Buffer
				raw.WriteString("Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=file.bin\r\nContent-Transfer-Encoding: " + encoding + "\r\n\r\n")
				if encoding == "base64" {
					encoder := base64.NewEncoder(base64.StdEncoding, &raw)
					_, _ = encoder.Write(payload)
					_ = encoder.Close()
				} else {
					raw.Write(payload)
				}
				raw.WriteString("\r\n--x--\r\n")
				_, _, download, err := extractMIMEPart(raw.Bytes(), "1")
				if size > MaxAttachmentBytes {
					if !errors.Is(err, ErrAttachmentTooLarge) || download != nil {
						t.Fatalf("want explicit limit error, got %v", err)
					}
					return
				}
				if err != nil || sha256.Sum256(download) != sha256.Sum256(payload) {
					t.Fatalf("download truncated or changed, length=%d err=%v", len(download), err)
				}
			})
		}
	}
}

func TestDraftReplacementReportsPendingCleanup(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &imapConn{conn: client, r: bufio.NewReader(client), timeout: time.Second}
	defer c.Close()
	finished := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				finished <- err
				return
			}
			fields := strings.Fields(line)
			tag := fields[0]
			switch {
			case strings.Contains(line, " LIST "):
				_, _ = io.WriteString(server, "* LIST (\\Drafts) \"/\" Drafts\r\n"+tag+" OK listed\r\n")
			case strings.Contains(line, " SELECT "):
				_, _ = io.WriteString(server, "* OK [UIDVALIDITY 9] identity\r\n"+tag+" OK selected\r\n")
			case strings.Contains(line, " APPEND "):
				n, ok := trailingLiteralSize(strings.TrimSpace(line))
				if !ok {
					finished <- fmt.Errorf("bad literal header")
					return
				}
				_, _ = io.WriteString(server, "+ continue\r\n")
				body := make([]byte, n)
				_, _ = io.ReadFull(r, body)
				suffix := make([]byte, 2)
				_, _ = io.ReadFull(r, suffix)
				if string(suffix) != "\r\n" {
					finished <- fmt.Errorf("bad terminator")
					return
				}
				_, _ = io.WriteString(server, tag+" OK [APPENDUID 9 77] appended\r\n")
			case strings.Contains(line, " UID STORE "):
				_, _ = io.WriteString(server, tag+" NO cannot delete old draft\r\n")
				finished <- nil
				return
			}
		}
	}()
	result, err := c.SaveDraft(context.Background(), "alice@example.test", Outgoing{Text: "new content"}, 42, 9)
	if err != nil || result.UID != 77 || result.UIDValidity != 9 || !result.CleanupPending {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

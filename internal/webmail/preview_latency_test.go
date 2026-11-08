package webmail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The latency measurement below answers one question: what does a page of
// snippets cost on top of a plain listing? The fake IMAP server sleeps
// previewLatencyRTT before answering every command, which models the network
// round trip of a remote server; wall-clock differences between
// Preview=false and Preview=true therefore expose exactly the extra IMAP
// work. The claimed overhead is one additional command per page, which the
// previewFetchCommands counter asserts deterministically.

const (
	previewLatencyRTT       = 5 * time.Millisecond
	previewLatencyLatencies = 30
)

// TestPreviewListLatencyMeasurement measures listing latency with and without
// previews against a loopback IMAP server with a simulated per-command round
// trip and logs P50/P95 for both modes.
func TestPreviewListLatencyMeasurement(t *testing.T) {
	const messages = 20
	uids := make([]uint32, 0, messages)
	bodies := map[uint32][]byte{}
	for uid := 1; uid <= messages; uid++ {
		uids = append(uids, uint32(uid))
		bodies[uint32(uid)] = []byte(fmt.Sprintf(
			"Subject: latency %d\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPreview body of message %d\r\nSecond line\r\n", uid, uid))
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var previewFetchCommands atomic.Int64
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
			time.Sleep(previewLatencyRTT)
			switch {
			case strings.Contains(line, "SELECT"):
				_, _ = io.WriteString(w, "* OK [UIDVALIDITY 7] selected\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "UID SEARCH"):
				_, _ = io.WriteString(w, "* SEARCH "+itoaJoin(uids)+"\r\n"+tag+" OK\r\n")
			case strings.Contains(line, "BODY.PEEK"):
				previewFetchCommands.Add(1)
				writePreviewFetch(w, line, tag, bodies)
			case strings.Contains(line, "UID FETCH"):
				for _, uid := range uids {
					_, _ = fmt.Fprintf(w, "* 1 FETCH (UID %d FLAGS () RFC822.SIZE %d ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n", uid, len(bodies[uid]))
				}
				_, _ = io.WriteString(w, tag+" OK\r\n")
			default:
				_, _ = io.WriteString(w, tag+" OK\r\n")
			}
			_ = w.Flush()
			if strings.Contains(line, "LOGOUT") {
				return
			}
		}
	}()

	client := dialTestClient(t, ln.Addr().String())
	measure := func(preview bool) []time.Duration {
		t.Helper()
		samples := make([]time.Duration, 0, previewLatencyLatencies)
		for i := 0; i < previewLatencyLatencies; i++ {
			start := time.Now()
			if _, err := client.ListMessages(context.Background(), "INBOX", ListOpts{Preview: preview, Limit: messages}); err != nil {
				t.Fatalf("list: %v", err)
			}
			samples = append(samples, time.Since(start))
		}
		return samples
	}

	baseline := measure(false)
	withPreview := measure(true)

	got := previewFetchCommands.Load()
	if got != previewLatencyLatencies {
		t.Fatalf("preview fetch commands = %d, want exactly %d (one per page)", got, previewLatencyLatencies)
	}

	baseP50, baseP95 := percentileOf(baseline, 50), percentileOf(baseline, 95)
	prevP50, prevP95 := percentileOf(withPreview, 50), percentileOf(withPreview, 95)
	t.Logf("latency of listing %d messages, %d iterations, simulated RTT %v per IMAP command:",
		messages, previewLatencyLatencies, previewLatencyRTT)
	t.Logf("  without preview: P50=%v P95=%v", baseP50, baseP95)
	t.Logf("  with    preview: P50=%v P95=%v (delta P50=%v, delta P95=%v)",
		prevP50, prevP95, prevP50-baseP50, prevP95-baseP95)
	t.Logf("  extra IMAP command per page: %d bytes/message budget, %d bytes page cap",
		previewFetchBytes, previewPageBytes)
}

// percentileOf returns the nearest-rank percentile of sampled durations.
func percentileOf(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

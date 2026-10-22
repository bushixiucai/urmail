// Package agent is the client side of the privileged agent channel.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/bushixiucai/urmail/internal/agentd"
)

// Client talks to the privileged agent over a unix socket.
type Client struct {
	socketPath string
	timeout    time.Duration
}

// NewClient builds a client for the given socket.
func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath, timeout: 30 * time.Second}
}

// Do performs one operation and returns the response.
//
// The request id is derived from the operation and a counter so that the
// agent's audit log can be correlated with the caller's log.
func (c *Client) Do(ctx context.Context, op string, params any) (*agentd.Response, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params for %s: %w", op, err)
	}

	request := agentd.Request{
		Version: agentd.ProtocolVersion,
		ID:      fmt.Sprintf("%s-%d", op, time.Now().UnixNano()),
		Op:      op,
		Params:  raw,
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal request for %s: %w", op, err)
	}
	payload = append(payload, '\n')

	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial agent socket: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write agent request: %w", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read agent response: %w", err)
	}

	var response agentd.Response
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		return nil, fmt.Errorf("decode agent response: %w", err)
	}
	return &response, nil
}

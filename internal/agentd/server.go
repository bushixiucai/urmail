package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// maxRequestLineBytes bounds one JSON request line. A local peer cannot
	// make the agent allocate unbounded memory before the request is parsed.
	maxRequestLineBytes = 16 << 20

	// readTimeout is the per-request idle budget. It is re-armed before every
	// read, so a client that sends a request starts a fresh budget; a client
	// that connects and stays silent is disconnected.
	readTimeout = 60 * time.Second

	// writeTimeout bounds writing one response.
	writeTimeout = 60 * time.Second
)

// errRequestTooLarge is returned when a request line exceeds the configured
// limit.
var errRequestTooLarge = errors.New("agent request line exceeds limit")

// Server serves privileged operations over a unix socket.
type Server struct {
	socketPath string
	registry   *Registry
	logger     *slog.Logger
	listener   net.Listener
	peerPolicy *peerPolicy
	wg         sync.WaitGroup
}

// NewServer builds a server bound to a socket path.
//
// The peer policy is loaded once: the agent's own UID and, when the account
// exists, the mailinabox service account are allowed to connect.
func NewServer(socketPath string, registry *Registry, logger *slog.Logger) *Server {
	policy, err := loadPeerPolicy()
	if err != nil && logger != nil {
		logger.Warn("agent peer policy is incomplete", "error", err)
	}
	return &Server{
		socketPath: socketPath,
		registry:   registry,
		logger:     logger,
		peerPolicy: policy,
	}
}

// Listen creates the socket and starts accepting connections.
//
// The socket is created with mode 0660. Together with the systemd unit
// (User=root Group=mailinabox, RuntimeDirectoryMode=0770) the socket is
// reachable by root and the mailinabox group only. The parent directory must
// already exist.
func (s *Server) Listen(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	// A stale socket from a crashed agent would make Listen fail.
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		_ = listener.Close()
		return fmt.Errorf("set socket permissions: %w", err)
	}
	s.listener = listener

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
	}()
	return nil
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		// Authentication happens before any request byte is interpreted.
		// A rejected peer is closed and never reaches a handler.
		if unixConn, ok := conn.(*net.UnixConn); ok {
			if err := authorizePeer(unixConn, s.peerPolicy); err != nil {
				s.logger.Warn("rejected agent connection", "error", err)
				_ = conn.Close()
				continue
			}
		} else {
			s.logger.Warn("rejected non-unix agent connection")
			_ = conn.Close()
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// Wait blocks until every connection handler has finished.
func (s *Server) Wait() { s.wg.Wait() }

// handle serves one connection. The protocol is one JSON request per line
// and one JSON response per line.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReaderSize(conn, 64*1024)
	encoder := json.NewEncoder(conn)

	for {
		// Per-request deadline: an idle or slow client cannot pin a handler
		// indefinitely, and a long-lived connection gets a fresh budget for
		// each request.
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))

		line, err := readRequestLine(reader, maxRequestLineBytes)
		if err != nil {
			if errors.Is(err, errRequestTooLarge) {
				if !s.writeResponse(conn, encoder, Response{
					Version: ProtocolVersion,
					Error:   &Error{Code: CodeBadRequest, Message: ErrBadRequest.Error()},
				}) {
					return
				}
			} else if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("agent read failed", "error", err)
			}
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			if !s.writeResponse(conn, encoder, Response{
				Version: ProtocolVersion,
				Error:   &Error{Code: CodeBadRequest, Message: ErrBadRequest.Error()},
			}) {
				return
			}
			continue
		}

		response := s.execute(request)
		s.logger.Info("agent operation",
			"op", request.Op,
			"request_id", request.ID,
			"ok", response.OK,
			"error_code", errorCode(response),
		)

		if !s.writeResponse(conn, encoder, response) {
			return
		}
	}
}

// execute runs one operation with panic recovery. A buggy operation must not
// take down the daemon or kill the goroutine serving other requests.
func (s *Server) execute(request Request) (response Response) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("agent operation panicked",
				"op", request.Op,
				"request_id", request.ID,
				"panic", recovered,
			)
			response = Response{
				Version: ProtocolVersion,
				ID:      request.ID,
				Error:   &Error{Code: CodeFailed, Message: ErrFailed.Error()},
			}
		}
	}()
	return s.registry.Execute(request)
}

// writeResponse encodes one response under a write deadline.
func (s *Server) writeResponse(conn net.Conn, encoder *json.Encoder, response Response) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := encoder.Encode(response); err != nil {
		s.logger.Warn("agent write failed", "error", err)
		return false
	}
	return true
}

// readRequestLine reads one newline-terminated request line without buffering
// more than limit bytes. bufio.Reader.ReadSlice is used so that an oversized
// line is rejected as soon as it crosses the limit instead of growing without
// bound.
func readRequestLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errRequestTooLarge
		}
		line = append(line, fragment...)

		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

// errorCode returns the error code of a response, or an empty string.
func errorCode(response Response) string {
	if response.Error == nil {
		return ""
	}
	return response.Error.Code
}

package agentd

import (
	"bufio"
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

// Server serves privileged operations over a unix socket.
type Server struct {
	socketPath string
	registry   *Registry
	logger     *slog.Logger
	listener   net.Listener
	wg         sync.WaitGroup
}

// NewServer builds a server bound to a socket path.
func NewServer(socketPath string, registry *Registry, logger *slog.Logger) *Server {
	return &Server{socketPath: socketPath, registry: registry, logger: logger}
}

// Listen creates the socket and starts accepting connections.
//
// The socket is created with mode 0660 so that only the agent's group (and
// root) can reach it. The parent directory must already exist.
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

	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	reader := bufio.NewReader(conn)
	encoder := json.NewEncoder(conn)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("agent read failed", "error", err)
			}
			return
		}
		if len(line) == 0 {
			continue
		}

		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			response := Response{
				Version: ProtocolVersion,
				Error:   &Error{Code: CodeBadRequest, Message: ErrBadRequest.Error()},
			}
			if err := encoder.Encode(response); err != nil {
				return
			}
			continue
		}

		response := s.registry.Execute(request)
		s.logger.Info("agent operation",
			"op", request.Op,
			"request_id", request.ID,
			"ok", response.OK,
			"error_code", errorCode(response),
		)

		if err := encoder.Encode(response); err != nil {
			return
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

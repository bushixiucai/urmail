package policy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
)

// Server runs the unix socket policy listener.
type Server struct {
	SocketPath string
	Service    *Service
	listener   net.Listener
}

// NewServer builds a policy server.
func NewServer(socketPath string, svc *Service) *Server {
	return &Server{
		SocketPath: socketPath,
		Service:    svc,
	}
}

// Start starts listening on the unix socket.
func (s *Server) Start(ctx context.Context) error {
	_ = os.Remove(s.SocketPath)
	l, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	s.listener = l

	// Restrict permissions to owner and group (postfix)
	_ = os.Chmod(s.SocketPath, 0660)

	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)

	for {
		req, err := ReadRequest(r)
		if err != nil || req == nil {
			return
		}
		decision := s.Service.Evaluate(ctx, req)
		if err := WriteDecision(conn, decision); err != nil {
			return
		}
	}
}

// Close stops listening and removes the socket.
func (s *Server) Close() error {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	return os.Remove(s.SocketPath)
}

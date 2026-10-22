package policy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.SocketPath)
	l, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	s.listener = l

	// The socket is 0660 and belongs to the postfix group so smtpd can
	// connect; urmaild must be in that group (SupplementaryGroups=postfix).
	_ = os.Chmod(s.SocketPath, 0o660)
	if group, err := user.LookupGroup("postfix"); err == nil {
		if gid, err := strconv.Atoi(group.Gid); err == nil {
			_ = os.Chown(s.SocketPath, -1, gid)
		}
	}

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

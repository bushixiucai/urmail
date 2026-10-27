package eas

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Content types and header names fixed by MS-ASHTTP.
const (
	ContentTypeWBXML   = "application/vnd.ms-sync.wbxml"
	headerVersion      = "MS-ASProtocolVersion"
	headerVersions     = "MS-ASProtocolVersions"
	headerCommands     = "MS-ASProtocolCommands"
	maxRequestBodySize = 24 << 20 // 24 MiB encoded MIME transport ceiling
)

// SyncHandler is the EAS-003 seam. The transport dispatches Sync requests to
// it together with the authenticated session, device id and the per-request
// email source.
type SyncHandler interface {
	// HandleSync processes one Sync request WBXML document and returns the
	// response document. Return the response even on partial failure so the
	// transport can still emit a protocol-visible Status.
	HandleSync(ctx context.Context, req *SyncRequest) (*Node, error)
}

// NotImplementableSync is a refusal backend. It answers with a Sync response
// whose top-level Status is 4 (protocol error per MS-ASCMD), clearly
// documented as "data sync not implemented here", instead of returning a
// fake successful empty sync. The production Server wires SyncBackend.
type NotImplementableSync struct{}

// HandleSync implements SyncHandler with the documented refusal.
func (NotImplementableSync) HandleSync(_ context.Context, _ *SyncRequest) (*Node, error) {
	resp := Element(pageAirSync, tokSync)
	// Status 4 = protocol error. Comment ties this to the unimplemented
	// backend so nobody mistakes it for an empty-but-successful sync.
	resp.Add(Text(pageAirSync, tokStatus, syncStatusProtocolError))
	return resp, nil
}

// EmailFolderLister produces the email half of the exposed hierarchy from the
// live IMAP namespace. Implementations must only return folders owned by the
// session mailbox.
type EmailFolderLister func(ctx context.Context, sess Session, tokenHash string) ([]Folder, error)

// Server is the EAS transport. Construct with NewServer.
type Server struct {
	Pool          *db.Pool
	Submitter     webmail.Submitter
	Dialer        webmail.Dialer
	MaySend       func(context.Context, int64, int64) (bool, error)
	SenderAllowed func(context.Context, int64, string, string) (bool, error)
	// Vault resolves the IMAP secret for the bearer token; required only when
	// EmailFolders is set.
	Vault webmail.CredentialVault
	// EmailFolders lists IMAP folders; when nil the hierarchy contains only
	// the collab addressbook and calendar.
	EmailFolders EmailFolderLister
	// Sync is the EAS-003 backend; nil uses NotImplementableSync.
	Sync SyncHandler
	// Mail builds the per-request IMAP email source for Sync. When nil,
	// email collections answer the documented server-error status and
	// collab collections keep working.
	Mail MailSourceFactory
	// PingBudget caps the Ping long poll; the request heartbeat is clamped to
	// this. Zero uses DefaultPingBudget.
	PingBudget time.Duration
	// PollInterval is how often the change log is re-checked during Ping.
	PollInterval time.Duration
	// Logger receives transport-level errors.
	Logger *slog.Logger
}

// DefaultPingBudget matches the task requirement of a ~10s server-side wait.
const DefaultPingBudget = 10 * time.Second

// NewServer returns a Server with defaults applied.
func NewServer(pool *db.Pool) *Server {
	return &Server{
		Pool:         pool,
		Sync:         &SyncBackend{Pool: pool, MaxItems: DefaultMaxItems, MaxResponseBytes: DefaultMaxResponseBytes},
		PingBudget:   DefaultPingBudget,
		PollInterval: 500 * time.Millisecond,
	}
}

// HandleOptions advertises exactly the negotiated versions and commands this
// transport accepts (MS-ASHTTP capability negotiation).
func (s *Server) HandleOptions(c *gin.Context) {
	if _, ok := s.requireSession(c); !ok {
		return
	}
	c.Header(headerVersions, AdvertisedVersions)
	c.Header(headerCommands, AdvertisedCommands)
	c.Status(http.StatusOK)
}

func (s *Server) requireSession(c *gin.Context) (Session, bool) {
	sess, ok := middleware.GetMailboxSession(c)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return Session{}, false
	}
	return sess, true
}

// HandleCommand serves the single EAS POST endpoint. Order of checks follows
// MS-ASHTTP: session, protocol version, then command routing.
func (s *Server) HandleCommand(c *gin.Context) {
	sess, ok := s.requireSession(c)
	if !ok {
		return
	}
	version := c.GetHeader(headerVersion)
	if !VersionSupported(version) {
		// Unknown/unsupported negotiated version: reject before any command
		// processing. 400 per MS-ASHTTP error handling.
		c.String(http.StatusBadRequest, "unsupported MS-ASProtocolVersion")
		return
	}
	cmd := c.Query("Cmd")
	deviceID := c.Query("DeviceId")
	if deviceID == "" || len(deviceID) > 200 {
		c.String(http.StatusBadRequest, "DeviceId required")
		return
	}
	if refusedCommands[cmd] {
		// Outside the implemented subset: refuse without device state writes
		// and without any protocol success. 403 per MS-ASHTTP server refusal.
		s.logWarn("refused command", "cmd", cmd, "device", deviceID)
		c.Status(http.StatusForbidden)
		return
	}
	switch cmd {
	case CmdFolderSync, CmdProvision, CmdPing, CmdSync, CmdSendMail, CmdSmartReply, CmdSmartForward, CmdItemOperations:
	default:
		c.String(http.StatusBadRequest, "unknown command")
		return
	}
	// Persist/refresh device state inside the session mailbox scope. A device
	// id therefore never gains access to another mailbox's rows.
	ua := c.GetHeader("User-Agent")
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if err := UpsertDevice(c.Request.Context(), s.Pool, sess, Device{DeviceID: deviceID, ProtocolVersion: version, UserAgent: ua}); err != nil {
		s.logError(c, "persist device", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	body, err := readBody(c)
	if err != nil {
		c.String(http.StatusBadRequest, "unreadable body")
		return
	}
	var req *Node
	if cmd == CmdPing && len(body) == 0 {
		req = Element(pagePing, tokPing)
	} else {
		req, err = Decode(body)
	}
	if err != nil {
		s.logWarn("bad wbxml", "cmd", cmd, "err", err)
		c.String(http.StatusBadRequest, "undecodable WBXML")
		return
	}
	if cmd == CmdSendMail || cmd == CmdSmartReply || cmd == CmdSmartForward {
		s.handleSendMail(c, sess, deviceID, req)
		return
	}
	var resp *Node
	switch cmd {
	case CmdItemOperations:
		resp, err = s.handleItemOperations(c.Request.Context(), sess, deviceID, req)
	case CmdFolderSync:
		resp, err = s.handleFolderSync(c.Request.Context(), sess, deviceID, req)
	case CmdProvision:
		resp, err = s.handleProvision(c.Request.Context(), sess, deviceID, req)
	case CmdPing:
		resp, err = s.handlePing(c.Request.Context(), sess, deviceID, req)
	case CmdSync:
		var mail MailSource
		if s.Sync != nil {
			if backend, ok := s.Sync.(interface{ wantsMail() bool }); ok && backend.wantsMail() && s.Mail != nil {
				mail, err = s.Mail(c.Request.Context(), sess)
				if err != nil {
					s.logError(c, "mail source", err)
					c.Status(http.StatusInternalServerError)
					return
				}
			}
		}
		if mail != nil {
			defer func() { _ = mail.Close() }()
		}
		resp, err = s.Sync.HandleSync(c.Request.Context(), &SyncRequest{
			Session: sess, DeviceID: deviceID, Node: req, Mail: mail,
		})
	}
	if err != nil {
		s.logError(c, "command failed", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	if resp == nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusOK, ContentTypeWBXML, Encode(resp))
}

// readBody reads and transparently decompresses a gzip-encoded request body.
func readBody(c *gin.Context) ([]byte, error) {
	r := io.LimitReader(c.Request.Body, maxRequestBodySize+1)
	if strings.EqualFold(c.GetHeader("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		defer func() { _ = zr.Close() }()
		r = io.LimitReader(zr, maxRequestBodySize+1)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBodySize {
		return nil, errors.New("body over transport ceiling")
	}
	return body, nil
}

func (s *Server) logWarn(msg string, args ...any) {
	if s.Logger != nil {
		s.Logger.Warn(msg, args...)
	}
}

func (s *Server) logError(c *gin.Context, msg string, err error) {
	if s.Logger != nil {
		s.Logger.Error(msg, "err", err, "path", c.FullPath())
	}
}

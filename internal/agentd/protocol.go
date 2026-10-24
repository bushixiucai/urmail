// Package agentd implements the privileged side of urMail.cn.
//
// The agent runs as root and is the only component that may touch system
// configuration. It listens on a unix socket and accepts *structured* requests
// — never a shell string — so that the set of things it can do is enumerable
// and testable.
package agentd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bushixiucai/urmail/internal/config"
)

// ProtocolVersion identifies the supported protocol version. The agent refuses requests with an unknown version so
// that a newer urmaild cannot silently drive an older agent into an
// unexpected operation.
const ProtocolVersion = 1

// Errors returned to clients.
var (
	// ErrUnknownOp is returned when the requested operation is not in the
	// whitelist.
	ErrUnknownOp = errors.New("unknown operation")
	// ErrBadRequest is returned when the request is malformed.
	ErrBadRequest = errors.New("malformed request")
	// ErrUnsupportedVersion is returned on a protocol mismatch.
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
	// ErrRejected is returned when an operation is known but its parameters
	// fail validation.
	ErrRejected = errors.New("operation rejected")
	// ErrFailed is returned when an operation ran but did not succeed.
	ErrFailed = errors.New("operation failed")
)

// Request is one privileged operation request.
type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params"`
}

// Response is the reply to a Request.
type Response struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Error describes a failed operation.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes.
const (
	CodeRejected        = "AGENT_OP_REJECTED"
	CodeUnknownOp       = "AGENT_UNKNOWN_OP"
	CodeBadRequest      = "AGENT_BAD_REQUEST"
	CodeVersionMismatch = "AGENT_VERSION_MISMATCH"
	CodeFailed          = "AGENT_OP_FAILED"
)

// Operation is a privileged action.
//
// The handler receives the raw parameters and returns stdout and stderr.
// Validation lives in the operation itself so that every rule is testable
// without touching the system.
type Operation func(params json.RawMessage) (stdout string, stderr string, err error)

// Registry holds the whitelist of privileged operations.
type Registry struct {
	ops         map[string]Operation
	quarantined func() bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{ops: map[string]Operation{}, quarantined: config.RestoreQuarantined}
}

// Register adds an operation to the whitelist.
func (r *Registry) Register(name string, op Operation) {
	r.ops[name] = op
}

// Has reports whether an operation is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.ops[name]
	return ok
}

// Execute runs a registered operation.
func (r *Registry) Execute(req Request) Response {
	resp := Response{Version: ProtocolVersion, ID: req.ID}

	if req.Version != ProtocolVersion {
		resp.Error = &Error{Code: CodeVersionMismatch, Message: ErrUnsupportedVersion.Error()}
		return resp
	}

	op, ok := r.ops[req.Op]
	if !ok {
		resp.Error = &Error{Code: CodeUnknownOp, Message: fmt.Sprintf("%s: %s", ErrUnknownOp.Error(), req.Op)}
		return resp
	}

	if r.quarantined != nil && r.quarantined() && !quarantineReadOnly(req.Op) {
		resp.Error = &Error{Code: CodeRejected, Message: "restore quarantine blocks privileged mutation"}
		return resp
	}
	stdout, stderr, err := op(req.Params)
	if err != nil {
		code := CodeFailed
		if errors.Is(err, ErrRejected) {
			code = CodeRejected
		}
		resp.Error = &Error{Code: code, Message: err.Error()}
		resp.Stderr = stderr
		return resp
	}

	resp.OK = true
	resp.Stdout = stdout
	resp.Stderr = stderr
	return resp
}

// NewProductionRegistry exposes only typed, root-controlled capabilities.
func NewProductionRegistry(manager *ApplyManager) *Registry {
	registry := NewRegistry()
	registry.quarantined = func() bool {
		return config.RestoreQuarantined() || (manager != nil && manager.cfg != nil && manager.cfg.Quarantined)
	}
	registry.Register(OpHostObserve, manager.ObserveHost)
	registry.Register(OpMailLogRead, ReadMailLog)
	registry.Register(OpPostfixQueue, ReadPostfixQueue)
	registry.Register(OpMailboxExpunge, manager.ExpungeRecall)
	registry.Register(OpSieveActivate, manager.ActivateSieve)
	registry.Register(OpConfigApply, manager.Apply)
	registry.Register(OpConfigStatus, manager.Status)
	registry.Register(OpCertificateStatus, manager.CertificateStatus)
	registry.Register(OpCertificateIssue, manager.IssueCertificate)
	registry.Register(OpCertificateRenew, manager.RenewCertificates)
	return registry
}

func quarantineReadOnly(op string) bool {
	switch op {
	case OpConfigStatus, OpMailLogRead, OpPostfixQueue, OpHostObserve, OpCertificateStatus:
		return true
	default:
		return false
	}
}

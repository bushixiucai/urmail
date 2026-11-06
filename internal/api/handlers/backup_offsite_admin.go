package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/store"
)

// probeTimeout bounds one test-connection roundtrip.
const probeTimeout = 30 * time.Second

// testBodyLimit bounds the test-connection request body.
const testBodyLimit = 1 << 16

// BackupOffsiteAdminHandler exposes the platform-admin-configured optional
// offsite backup destination (BE-04). Credentials arrive as a JSON object,
// are immediately encrypted with the master key and persist as enc:v1:
// ciphertext; they are never returned, logged or audited.
type BackupOffsiteAdminHandler struct {
	Store   *store.Queries
	Secrets SecretsBox
}

// offsiteDestinationInput is the request shape for PUT and test-connection.
type offsiteDestinationInput struct {
	Type         string                             `json:"type"`
	Endpoint     string                             `json:"endpoint"`
	Bucket       string                             `json:"bucket"`
	Prefix       string                             `json:"prefix"`
	Enabled      bool                               `json:"enabled"`
	AgeRecipient string                             `json:"age_recipient"`
	Credentials  *backupauto.DestinationCredentials `json:"credentials"`
}

func (i offsiteDestinationInput) toDestination() backupauto.Destination {
	return backupauto.Destination{
		Type:         i.Type,
		Endpoint:     i.Endpoint,
		Bucket:       i.Bucket,
		Prefix:       i.Prefix,
		Enabled:      i.Enabled,
		AgeRecipient: i.AgeRecipient,
	}
}

func (h *BackupOffsiteAdminHandler) loadDestination(c *gin.Context) (backupauto.Destination, bool, error) {
	q := requestStore(c, h.Store)
	row, err := q.PlatformGetSetting(c, backupauto.DestinationSettingsKey)
	if errors.Is(err, sql.ErrNoRows) {
		return backupauto.Destination{}, false, nil
	}
	if err != nil {
		return backupauto.Destination{}, false, err
	}
	var destination backupauto.Destination
	if err := json.Unmarshal(row.Value, &destination); err != nil {
		return backupauto.Destination{}, false, err
	}
	return destination, true, nil
}

// Get returns the credential-free destination status. An unconfigured
// destination is an explicit disabled state, never an error.
func (h *BackupOffsiteAdminHandler) Get(c *gin.Context) {
	destination, configured, err := h.loadDestination(c)
	if err != nil || !configured {
		summary := map[string]bool{}
		if err != nil {
			summary = map[string]bool{"unreadable": true}
		}
		c.JSON(http.StatusOK, gin.H{"configured": err != nil, "enabled": false, "has_credentials": false, "credential_summary": summary})
		return
	}
	status := destination.StatusOf()
	status.CredentialSummary = h.credentialSummary(destination)
	c.JSON(http.StatusOK, status)
}

// credentialSummary decrypts the stored blob to report which credential
// kinds exist. It never exposes values; decryption failure degrades to an
// "unreadable" marker instead of leaking anything.
func (h *BackupOffsiteAdminHandler) credentialSummary(destination backupauto.Destination) map[string]bool {
	if destination.Credentials == "" {
		return map[string]bool{}
	}
	if h.Secrets == nil {
		return map[string]bool{"unreadable": true}
	}
	plain, err := h.Secrets.DecryptString(destination.Credentials)
	if err != nil {
		return map[string]bool{"unreadable": true}
	}
	var dc backupauto.DestinationCredentials
	if json.Unmarshal([]byte(plain), &dc) != nil {
		return map[string]bool{"unreadable": true}
	}
	summary := map[string]bool{}
	switch destination.Type {
	case "s3":
		summary["access_key"] = dc.S3.AccessKeyID != ""
		summary["secret_key"] = dc.S3.SecretKey != ""
	case "sftp":
		summary["username"] = dc.SFTPUser != ""
		summary["password"] = dc.SFTP.Password != ""
		summary["private_key"] = dc.SFTP.PrivateKeyPath != ""
		summary["host_key_pinned"] = dc.SFTP.HostKeyFingerprint != ""
	case "webdav":
		summary["username"] = dc.WebDAV.Username != ""
		summary["password"] = dc.WebDAV.Password != ""
	}
	return summary
}

// encryptCredentials marshals and seals the request credentials, enforcing
// the per-type completeness policy. It returns an empty string when the
// type needs no credentials.
func encryptCredentials(box SecretsBox, input offsiteDestinationInput) (string, error) {
	if input.Credentials == nil {
		return "", errors.New("credentials required")
	}
	switch input.Type {
	case "sftp":
		if input.Credentials.SFTPUser == "" || (input.Credentials.SFTP.Password == "" && input.Credentials.SFTP.PrivateKeyPath == "") || input.Credentials.SFTP.HostKeyFingerprint == "" {
			return "", errors.New("sftp requires username, authentication and a pinned host key fingerprint")
		}
	case "s3":
		if input.Credentials.S3.AccessKeyID == "" || input.Credentials.S3.SecretKey == "" {
			return "", errors.New("s3 requires access keys")
		}
	case "webdav":
		// Anonymous WebDAV stays testable for development servers.
	default:
		return "", errors.New("unknown destination type")
	}
	raw, err := json.Marshal(input.Credentials)
	if err != nil {
		return "", err
	}
	cipher, err := box.EncryptString(string(raw))
	if err != nil || !strings.HasPrefix(cipher, "enc:v1:") {
		return "", errors.New("encrypt credentials")
	}
	return cipher, nil
}

// Put validates and persists the destination. type "" clears the stored
// destination (explicit disable). When enabled is false without new
// credentials the previously stored blob is preserved so an operator can
// re-enable without re-entering secrets. The response mirrors Get: no
// credential material, only presence flags.
func (h *BackupOffsiteAdminHandler) Put(c *gin.Context) {
	if h.Secrets == nil {
		authError(c, 503, "SECRETS_UNCONFIGURED")
		return
	}
	var input offsiteDestinationInput
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	if input.Type == "" {
		if input.Enabled || input.Endpoint != "" || input.Bucket != "" || input.Prefix != "" || input.Credentials != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if err := q.PlatformDeleteSetting(c, backupauto.DestinationSettingsKey); err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		c.JSON(http.StatusOK, gin.H{"configured": false, "enabled": false, "has_credentials": false})
		return
	}
	destination := input.toDestination()
	if err := destination.Validate(); err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	switch {
	case input.Credentials != nil:
		cipher, err := encryptCredentials(h.Secrets, input)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		destination.Credentials = cipher
	case input.Enabled:
		// Enabling without credentials would produce a broken destination.
		authError(c, 400, "INVALID_PARAMS")
		return
	default:
		// Disabled update without credentials: preserve the stored blob.
		saved, configured, err := h.loadDestination(c)
		if err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
		if configured && saved.Type == destination.Type {
			destination.Credentials = saved.Credentials
		}
	}
	value, err := json.Marshal(destination)
	if err != nil {
		authError(c, 500, "INTERNAL_ERROR")
		return
	}
	if err = q.PlatformUpsertSetting(c, store.PlatformUpsertSettingParams{Key: backupauto.DestinationSettingsKey, Value: value}); err != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	status := destination.StatusOf()
	status.CredentialSummary = h.credentialSummary(destination)
	c.JSON(http.StatusOK, status)
}

// TestConnection runs one full upload/download/delete probe. The body is
// either the full destination (with inline credentials, for testing before
// save) or empty (test the saved destination). The response carries at most
// endpoint-level error detail; credential values never appear because the
// transport contract keeps them out of URIs and error strings.
func (h *BackupOffsiteAdminHandler) TestConnection(c *gin.Context) {
	if h.Secrets == nil {
		authError(c, 503, "SECRETS_UNCONFIGURED")
		return
	}
	var destination backupauto.Destination
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		var input offsiteDestinationInput
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, testBodyLimit))
		if err != nil || json.Unmarshal(raw, &input) != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if input.Type == "" {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		destination = input.toDestination()
		if err := destination.Validate(); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "invalid destination"})
			return
		}
		cipher, err := encryptCredentials(h.Secrets, input)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "credentials required to test an unsaved destination"})
			return
		}
		destination.Credentials = cipher
	} else {
		saved, configured, err := h.loadDestination(c)
		if err != nil || !configured {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "destination not configured"})
			return
		}
		destination = saved
	}
	if !destination.Enabled {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "destination disabled"})
		return
	}
	transport, _, err := destination.Store(h.Secrets)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "invalid destination or credentials"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
	defer cancel()
	if err := backupauto.Probe(ctx, transport); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

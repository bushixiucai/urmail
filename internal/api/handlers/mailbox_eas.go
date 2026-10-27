package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/eas"
)

// EAS owns the ActiveSync transport once wired; nil keeps the pre-EAS-002
// stub behavior so older deployments stay valid.

// EASOptions advertises the negotiated versions and commands the transport
// actually accepts.
func (h MailboxNativeHandler) EASOptions(c *gin.Context) {
	if h.EAS == nil {
		if !easAuthenticated(c) {
			return
		}
		c.Header("MS-ASProtocolVersions", "")
		c.Header("MS-ASProtocolCommands", "")
		c.Status(http.StatusOK)
		return
	}
	h.EAS.HandleOptions(c)
}

// EASCommand dispatches the WBXML command endpoint to the transport.
func (h MailboxNativeHandler) EASCommand(c *gin.Context) {
	if h.EAS == nil {
		if !easAuthenticated(c) {
			return
		}
		authError(c, http.StatusNotImplemented, "EAS_NOT_IMPLEMENTED")
		return
	}
	h.EAS.HandleCommand(c)
}

// easAuthenticated reports a valid mailbox principal session.
func easAuthenticated(c *gin.Context) bool {
	if _, ok := middleware.GetMailboxSession(c); ok {
		return true
	}
	authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
	return false
}

// compile-time guard that the transport keeps the expected shape.
var _ = eas.AdvertisedVersions

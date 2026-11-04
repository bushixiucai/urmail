package handlers

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/ai"
)

func respondProbeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrProbeForbidden):
		authError(c, 403, "FORBIDDEN")
	case errors.Is(err, ErrProbeChanged):
		authError(c, 409, "PROBE_CONFIGURATION_CHANGED")
	default:
		authError(c, 503, "DB_ERROR")
	}
}
func (h *AIHealthHandler) fetchHealth(ctx context.Context, provider ai.Provider) ([]string, error) {
	if h.FetchHealthModels != nil {
		return h.FetchHealthModels(ctx, provider)
	}
	return ai.FetchModels(ctx, provider)
}

package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// OnboardingStore is the tenant-scoped read contract for onboarding status.
type OnboardingStore interface {
	ListOnboardingTasksByTenant(ctx context.Context, tenantID int64) ([]store.OnboardingTasks, error)
}

// OnboardingHandler exposes safe, tenant-scoped provisioning progress.
type OnboardingHandler struct{ Store OnboardingStore }

// OnboardingTaskResponse is the safe public shape of provisioning progress.
type OnboardingTaskResponse struct {
	ID                int64  `json:"id"`
	DomainID          *int64 `json:"domain_id"`
	OrderID           *int64 `json:"order_id"`
	Status            string `json:"status"`
	Step              string `json:"step"`
	Attempts          int32  `json:"attempts"`
	MaxAttempts       int32  `json:"max_attempts"`
	CreatedAt         any    `json:"created_at"`
	UpdatedAt         any    `json:"updated_at"`
	CompletedAt       any    `json:"completed_at"`
	NextAttemptAt     any    `json:"next_attempt_at"`
	ErrorCode         string `json:"error_code,omitempty"`
	DesiredGeneration string `json:"desired_generation,omitempty"`
}

// List returns this tenant's bounded asynchronous provisioning task history.
func (h *OnboardingHandler) List(c *gin.Context) {
	tenantID, ok := middleware.GetTenantID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "NO_TENANT_CONTEXT", "message": "auth.no_tenant"}})
		return
	}
	tasks, err := requestStore(c, h.Store).ListOnboardingTasksByTenant(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "ONBOARDING_LIST_FAILED", "message": "onboarding.list_failed"}})
		return
	}
	response := make([]OnboardingTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		item := OnboardingTaskResponse{ID: task.ID, Status: task.Status, Step: task.Step, Attempts: task.Attempts, MaxAttempts: task.MaxAttempts, NextAttemptAt: task.NextAttemptAt.Time, ErrorCode: task.LastErrorCode.String, DesiredGeneration: task.DesiredGeneration.String}
		if task.DomainID.Valid {
			value := task.DomainID.Int64
			item.DomainID = &value
		}
		if task.OrderID.Valid {
			value := task.OrderID.Int64
			item.OrderID = &value
		}
		if task.CreatedAt.Valid {
			item.CreatedAt = task.CreatedAt.Time
		}
		if task.UpdatedAt.Valid {
			item.UpdatedAt = task.UpdatedAt.Time
		}
		if task.CompletedAt.Valid {
			item.CompletedAt = task.CompletedAt.Time
		}
		response = append(response, item)
	}
	c.JSON(http.StatusOK, gin.H{"tasks": response})
}

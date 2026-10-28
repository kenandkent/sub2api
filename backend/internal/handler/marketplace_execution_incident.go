package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type marketplaceIncidentStore interface {
	RegisterExecutionIncident(context.Context, int64, string, int64, string, string, string) (*service.MarketplaceExecutionIncident, error)
	ListExecutionIncidents(context.Context, int64, string) ([]service.MarketplaceExecutionIncident, error)
}

// AdminRegisterExecutionIncident records an allegation for manual review only;
// it neither verifies usage nor triggers a payment or charge.
func (h *SupplierFinanceHandler) AdminRegisterExecutionIncident(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, ok := h.actor(c, true, true)
	if !ok {
		return
	}
	store, ok := h.recovery.(marketplaceIncidentStore)
	if !ok {
		response.Error(c, http.StatusServiceUnavailable, "Incident service unavailable")
		return
	}
	var req struct {
		AttemptID          int64  `json:"attempt_id"`
		ReleaseOperationID string `json:"release_operation_id"`
		Evidence           string `json:"evidence"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.BadRequest(c, "Invalid incident request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		response.BadRequest(c, "Invalid incident request")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	v, err := store.RegisterExecutionIncident(c.Request.Context(), actor, c.Param("id"), req.AttemptID, req.ReleaseOperationID, req.Evidence, key)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) AdminListExecutionIncidents(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, ok := h.actor(c, true, true)
	if !ok {
		return
	}
	store, ok := h.recovery.(marketplaceIncidentStore)
	if !ok {
		response.Error(c, http.StatusServiceUnavailable, "Incident service unavailable")
		return
	}
	items, err := store.ListExecutionIncidents(c.Request.Context(), actor, c.Param("id"))
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

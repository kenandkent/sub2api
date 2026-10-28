package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type MarketplaceVaultRotationHandler struct {
	store service.MarketplaceVaultRotationStore
	totp  *service.TotpService
	users *service.UserService
}

func NewMarketplaceVaultRotationHandler(store service.MarketplaceVaultRotationStore, totp *service.TotpService, users *service.UserService) *MarketplaceVaultRotationHandler {
	return &MarketplaceVaultRotationHandler{store: store, totp: totp, users: users}
}

func (h *MarketplaceVaultRotationHandler) Rotate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, ok := supplierAdmin(c)
	if !ok {
		return
	}
	if h == nil || h.store == nil || h.totp == nil || h.users == nil {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Vault rotation unavailable", "VAULT_OPERATION_UNKNOWN", nil)
		return
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return
	}
	var req struct {
		OperationID string `json:"operation_id"`
		Limit       int    `json:"limit"`
		Reason      string `json:"reason"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid vault rotation request", "VAULT_INPUT_INVALID", nil)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || req.OperationID == "" || strings.TrimSpace(req.OperationID) != req.OperationID || len(req.OperationID) > 255 || req.Reason == "" || strings.TrimSpace(req.Reason) != req.Reason || len(req.Reason) > 4096 || req.Limit < 1 || req.Limit > 200 {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid vault rotation request", "VAULT_INPUT_INVALID", nil)
		return
	}
	v, err := h.store.Rotate(c.Request.Context(), actor, req.OperationID, req.Reason, req.Limit)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVaultRotationConflict):
			response.ErrorWithDetails(c, http.StatusConflict, "Vault rotation operation conflicts", "VAULT_OPERATION_CONFLICT", nil)
		case errors.Is(err, service.ErrVaultRotationInvalid):
			response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Vault rotation outcome unavailable", "VAULT_OPERATION_UNKNOWN", nil)
		default:
			response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Vault rotation unavailable", "VAULT_OPERATION_UNKNOWN", nil)
		}
		return
	}
	response.Success(c, v)
}

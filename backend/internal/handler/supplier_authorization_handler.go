package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierAuthorizationHandler struct {
	svc *service.SupplierAuthorizationService
}

func NewSupplierAuthorizationHandler(svc *service.SupplierAuthorizationService) *SupplierAuthorizationHandler {
	return &SupplierAuthorizationHandler{svc: svc}
}
func supplierAuthorizationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrSupplierAuthorizationInvalid):
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid or expired authorization", "supplier_authorization_invalid", nil)
	case errors.Is(err, service.ErrSupplierAuthorizationUnavailable):
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Authorization temporarily unavailable", "supplier_authorization_unavailable", nil)
	case errors.Is(err, service.ErrSupplierAuthorizationInProgress):
		response.ErrorWithDetails(c, http.StatusConflict, "Authorization is processing", "supplier_authorization_processing", nil)
	case errors.Is(err, service.ErrSupplierAuthorizationNeedsReview):
		response.ErrorWithDetails(c, http.StatusConflict, "Authorization requires review", "supplier_authorization_needs_review", nil)
	case errors.Is(err, service.ErrSupplierAuthorizationFailed):
		response.ErrorWithDetails(c, http.StatusConflict, "Authorization failed", "supplier_authorization_failed", nil)
	default:
		response.ErrorFrom(c, err)
	}
}
func (h *SupplierAuthorizationHandler) Start(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		Provider  string `json:"provider"`
		Purpose   string `json:"purpose"`
		AccountID int64  `json:"account_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Provider != "openai" {
		response.BadRequest(c, "Invalid authorization request")
		return
	}
	result, err := h.svc.Start(c.Request.Context(), user, req.Purpose, req.AccountID)
	if err != nil {
		supplierAuthorizationError(c, err)
		return
	}
	response.Created(c, result)
}
func (h *SupplierAuthorizationHandler) Complete(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		State     string `json:"state"`
		Code      string `json:"code"`
		Purpose   string `json:"purpose"`
		AccountID int64  `json:"account_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid authorization request")
		return
	}
	result, err := h.svc.Complete(c.Request.Context(), user, c.Param("id"), req.State, req.Code, req.Purpose, req.AccountID)
	if err != nil {
		supplierAuthorizationError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *SupplierAuthorizationHandler) Status(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	result, err := h.svc.Status(c.Request.Context(), user, c.Param("id"))
	if errors.Is(err, service.ErrSupplierAuthorizationInvalid) {
		response.NotFound(c, "Authorization not found")
		return
	}
	if err != nil {
		supplierAuthorizationError(c, err)
		return
	}
	response.Success(c, result)
}

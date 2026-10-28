package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type fundingAdminService interface {
	ReclassifyLegacyFunding(context.Context, service.FundingReclassCommand) (*service.FundingReclassResult, error)
	ListFundingRisks(context.Context, int64) ([]service.FundingRisk, error)
	ClearFundingRisk(context.Context, service.FundingRiskClearCommand) (*service.FundingRiskClearResult, error)
}

// fundingAdminWriteError reports a no-effect decision only after the
// repository has checked the operation key and returned without committing.
// Conflicts and unknown errors retain the original operation for recovery.
func fundingAdminWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrFundingRiskStale):
		response.ErrorWithDetails(c, http.StatusConflict, "Funding risk version changed", "FUNDING_RISK_STALE", nil)
	case errors.Is(err, service.ErrFundingAdminConflict):
		response.ErrorWithDetails(c, http.StatusConflict, "Funding operation conflicts", "FUNDING_OPERATION_CONFLICT", nil)
	case errors.Is(err, service.ErrFundingAdminRejected):
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid funding request", "FUNDING_INPUT_INVALID", nil)
	case errors.Is(err, service.ErrInvalidFunding), errors.Is(err, service.ErrInsufficientFunding), errors.Is(err, sql.ErrNoRows):
		response.ErrorWithDetails(c, http.StatusBadRequest, "Funding operation was rejected", "FUNDING_OPERATION_REJECTED", nil)
	default:
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Funding operation outcome unavailable", "FUNDING_OPERATION_UNKNOWN", nil)
	}
}

func (h *PaymentFundingRecoveryHandler) Reclassify(c *gin.Context) {
	actor, ok := h.admin(c)
	if !ok {
		return
	}
	var req struct {
		UserID                       int64  `json:"user_id"`
		OrderID                      int64  `json:"order_id"`
		Amount                       string `json:"amount"`
		Evidence                     string `json:"evidence"`
		RemainingProvenanceConfirmed bool   `json:"remaining_provenance_confirmed"`
	}
	if c.ShouldBindJSON(&req) != nil || req.UserID <= 0 || req.OrderID <= 0 || !req.RemainingProvenanceConfirmed || !chargebackAmountPattern.MatchString(req.Amount) {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid funding reclassification", "FUNDING_INPUT_INVALID", nil)
		return
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || !amount.IsPositive() {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid funding reclassification", "FUNDING_INPUT_INVALID", nil)
		return
	}
	svc, ok := h.payments.(fundingAdminService)
	if !ok {
		response.Error(c, 503, "Funding administration unavailable")
		return
	}
	out, err := svc.ReclassifyLegacyFunding(c.Request.Context(), service.FundingReclassCommand{UserID: req.UserID, OrderID: req.OrderID, ActorID: actor, Amount: amount, Evidence: req.Evidence, RemainingProvenanceConfirmed: req.RemainingProvenanceConfirmed, OperationID: c.GetHeader("Idempotency-Key")})
	if err != nil {
		fundingAdminWriteError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *PaymentFundingRecoveryHandler) Risks(c *gin.Context) {
	actor, ok := h.admin(c)
	if !ok {
		return
	}
	svc, ok := h.payments.(fundingAdminService)
	if !ok {
		response.Error(c, 503, "Funding administration unavailable")
		return
	}
	out, err := svc.ListFundingRisks(c.Request.Context(), actor)
	if err != nil {
		response.BadRequest(c, "Funding risks could not be loaded")
		return
	}
	response.Success(c, out)
}
func (h *PaymentFundingRecoveryHandler) ClearRisk(c *gin.Context) {
	actor, ok := h.admin(c)
	if !ok {
		return
	}
	user, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || user <= 0 {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid funding risk", "FUNDING_INPUT_INVALID", nil)
		return
	}
	var req struct {
		ExpectedVersion int64  `json:"expected_version"`
		Evidence        string `json:"evidence"`
	}
	if c.ShouldBindJSON(&req) != nil || req.ExpectedVersion <= 0 || strings.TrimSpace(req.Evidence) == "" {
		response.ErrorWithDetails(c, http.StatusBadRequest, "Invalid funding risk review", "FUNDING_INPUT_INVALID", nil)
		return
	}
	svc, ok := h.payments.(fundingAdminService)
	if !ok {
		response.Error(c, 503, "Funding administration unavailable")
		return
	}
	out, err := svc.ClearFundingRisk(c.Request.Context(), service.FundingRiskClearCommand{UserID: user, ActorID: actor, ExpectedVersion: req.ExpectedVersion, Evidence: req.Evidence, OperationID: c.GetHeader("Idempotency-Key")})
	if err != nil {
		fundingAdminWriteError(c, err)
		return
	}
	response.Success(c, out)
}

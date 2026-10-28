package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"strings"
)

type MarketplaceRefundHandler struct {
	refunds *service.MarketplaceRefundService
	totp    *service.TotpService
	users   *service.UserService
}

func NewMarketplaceRefundHandler(refunds *service.MarketplaceRefundService) *MarketplaceRefundHandler {
	return &MarketplaceRefundHandler{refunds: refunds}
}

func (h *MarketplaceRefundHandler) SetStepUpDeps(totp *service.TotpService, users *service.UserService) {
	h.totp = totp
	h.users = users
}

// Refund must be registered under the administrator JWT group.
func (h *MarketplaceRefundHandler) Refund(c *gin.Context) {
	actor, ok := supplierAdmin(c)
	if !ok {
		return
	}
	if h.totp == nil || h.users == nil {
		response.Forbidden(c, "Step-up unavailable")
		return
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return
	}
	receipt, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "Invalid receipt")
		return
	}
	operation := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if operation == "" {
		response.BadRequest(c, "Idempotency-Key required")
		return
	}
	var req struct {
		Amount service.Money `json:"amount"`
		Reason string        `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid refund amount")
		return
	}
	if err = h.refunds.RefundWithReason(c.Request.Context(), receipt.String(), actor, req.Amount.Decimal, operation, req.Reason); err != nil {
		response.BadRequest(c, "Refund could not be completed")
		return
	}
	response.Success(c, gin.H{"receipt_id": receipt.String(), "amount": req.Amount})
}

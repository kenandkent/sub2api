package handler

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type fundingRecoveryService interface {
	ListPendingFundingRefunds(context.Context, int) ([]service.FundingRefundReviewItem, error)
	ResolveUncertainFunding(context.Context, service.UncertainFundingResolution) (*service.UncertainFundingResolutionResult, error)
}

type manualChargebackService interface {
	RecordExternalChargebackExact(context.Context, int64, int64, decimal.Decimal, string, string) error
}

var chargebackAmountPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,11})(?:\.[0-9]{1,8})?$`)

type PaymentFundingRecoveryHandler struct {
	payments fundingRecoveryService
	totp     *service.TotpService
	users    *service.UserService
}

func NewPaymentFundingRecoveryHandler(payments fundingRecoveryService, totp *service.TotpService, users *service.UserService) *PaymentFundingRecoveryHandler {
	return &PaymentFundingRecoveryHandler{payments: payments, totp: totp, users: users}
}
func (h *PaymentFundingRecoveryHandler) admin(c *gin.Context) (int64, bool) {
	actor, ok := supplierAdmin(c)
	if !ok {
		return 0, false
	}
	if h.users == nil || h.totp == nil || h.payments == nil {
		response.Error(c, 503, "Funding recovery unavailable")
		return 0, false
	}
	u, err := h.users.GetByID(c.Request.Context(), actor)
	if err != nil || u == nil || !u.IsAdmin() || !u.IsActive() {
		response.Forbidden(c, "Administrator required")
		return 0, false
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return 0, false
	}
	return actor, true
}
func (h *PaymentFundingRecoveryHandler) List(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			response.BadRequest(c, "Invalid limit")
			return
		}
	}
	v, err := h.payments.ListPendingFundingRefunds(c.Request.Context(), limit)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, v)
}
func (h *PaymentFundingRecoveryHandler) Resolve(c *gin.Context) {
	actor, ok := h.admin(c)
	if !ok {
		return
	}
	var req struct {
		OrderID          int64  `json:"order_id"`
		AttemptID        string `json:"attempt_id"`
		Decision         string `json:"decision"`
		ProviderRefundID string `json:"provider_refund_id"`
		Evidence         string `json:"evidence"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid recovery request")
		return
	}
	v, err := h.payments.ResolveUncertainFunding(c.Request.Context(), service.UncertainFundingResolution{OrderID: req.OrderID, AttemptID: req.AttemptID, ActorID: actor, Decision: req.Decision, ProviderRefundID: req.ProviderRefundID, Evidence: req.Evidence, OperationID: c.GetHeader("Idempotency-Key")})
	if err != nil {
		response.BadRequest(c, "Funding recovery could not be completed")
		return
	}
	response.Success(c, v)
}

// Chargeback registers a manually verified provider event; it never calls the bank.
func (h *PaymentFundingRecoveryHandler) Chargeback(c *gin.Context) {
	actor, ok := h.admin(c)
	if !ok {
		return
	}
	var req struct {
		OrderID         int64  `json:"order_id"`
		CreditedAmount  string `json:"credited_amount"`
		ProviderEventID string `json:"provider_event_id"`
		Evidence        string `json:"evidence"`
	}
	if c.ShouldBindJSON(&req) != nil || req.OrderID <= 0 || !chargebackAmountPattern.MatchString(req.CreditedAmount) {
		response.BadRequest(c, "Invalid chargeback request")
		return
	}
	amount, err := decimal.NewFromString(req.CreditedAmount)
	event, evidence := strings.TrimSpace(req.ProviderEventID), strings.TrimSpace(req.Evidence)
	if err != nil || !amount.IsPositive() || event == "" || len(event) > 255 || evidence == "" || len(evidence) > 4000 {
		response.BadRequest(c, "Invalid chargeback request")
		return
	}
	payments, ok := h.payments.(manualChargebackService)
	if !ok {
		response.Error(c, 503, "Chargeback recovery unavailable")
		return
	}
	if err := payments.RecordExternalChargebackExact(c.Request.Context(), req.OrderID, actor, amount, event, evidence); err != nil {
		response.BadRequest(c, "Chargeback could not be recorded")
		return
	}
	response.Success(c, gin.H{"recorded": true})
}

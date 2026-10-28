package handler

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierPayoutHandler struct {
	payouts *service.SupplierPayoutService
	totp    *service.TotpService
	users   *service.UserService
}

func NewSupplierPayoutHandler(payouts *service.SupplierPayoutService, totp *service.TotpService, users *service.UserService) *SupplierPayoutHandler {
	return &SupplierPayoutHandler{payouts: payouts, totp: totp, users: users}
}
func (h *SupplierPayoutHandler) actor(c *gin.Context, stepUp bool) (int64, bool) {
	if stepUp && !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return 0, false
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}
func payoutID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return 0, false
	}
	return id, true
}
func payoutResponse(c *gin.Context, v any, err error) {
	if err != nil {
		response.BadRequest(c, "Payout operation could not be completed")
		return
	}
	response.Success(c, v)
}
func (h *SupplierPayoutHandler) CreateDestination(c *gin.Context) {
	actor, ok := h.actor(c, true)
	if !ok {
		return
	}
	var req struct {
		MaskedLabel string `json:"masked_label" binding:"required"`
		Credential  string `json:"credential" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid destination")
		return
	}
	v, err := h.payouts.CreateDestination(c.Request.Context(), actor, req.MaskedLabel, []byte(req.Credential), c.GetHeader("Idempotency-Key"))
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) ListDestinations(c *gin.Context) {
	actor, ok := h.actor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.Destinations(c.Request.Context(), actor)
	payoutResponse(c, v, err)
}

// VerifyDestination must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) VerifyDestination(c *gin.Context) {
	actor, ok := h.actor(c, true)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	v, err := h.payouts.VerifyDestination(c.Request.Context(), id, actor, c.GetHeader("Idempotency-Key"))
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) Request(c *gin.Context) {
	actor, ok := h.actor(c, true)
	if !ok {
		return
	}
	var req struct {
		DestinationID int64         `json:"destination_id" binding:"required"`
		Amount        service.Money `json:"amount" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid payout")
		return
	}
	v, err := h.payouts.Request(c.Request.Context(), actor, req.DestinationID, req.Amount.Decimal, c.GetHeader("Idempotency-Key"))
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) List(c *gin.Context) {
	actor, ok := h.actor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.List(c.Request.Context(), actor)
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) Get(c *gin.Context) {
	actor, ok := h.actor(c, false)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	v, err := h.payouts.Get(c.Request.Context(), id, actor, false)
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) action(c *gin.Context, name string) {
	actor, ok := h.actor(c, true)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	var req struct {
		Evidence          string        `json:"evidence"`
		ExternalReference string        `json:"external_reference"`
		Currency          string        `json:"currency"`
		Amount            service.Money `json:"amount"`
		PaidAt            *time.Time    `json:"paid_at"`
	}
	if c.Request.ContentLength != 0 && c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid action")
		return
	}
	v, err := h.payouts.Action(c.Request.Context(), service.PayoutAction{PayoutID: id, ActorID: actor, OperationID: c.GetHeader("Idempotency-Key"), Action: name, Evidence: req.Evidence, ExternalReference: req.ExternalReference, Currency: req.Currency, Amount: req.Amount.Decimal, PaidAt: req.PaidAt})
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) Cancel(c *gin.Context) { h.action(c, "cancel") }

// The following actions must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) Approve(c *gin.Context) { h.action(c, "approve") }
func (h *SupplierPayoutHandler) Reject(c *gin.Context)  { h.action(c, "reject") }
func (h *SupplierPayoutHandler) Start(c *gin.Context)   { h.action(c, "start") }
func (h *SupplierPayoutHandler) Confirm(c *gin.Context) { h.action(c, "confirm") }
func (h *SupplierPayoutHandler) Fail(c *gin.Context)    { h.action(c, "fail") }

// AdminList must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) AdminList(c *gin.Context) {
	actor, ok := h.actor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.ListAdmin(c.Request.Context(), actor)
	payoutResponse(c, v, err)
}

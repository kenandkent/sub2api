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
	if stepUp && (h.totp == nil || h.users == nil) {
		response.Forbidden(c, "Step-up unavailable")
		return 0, false
	}
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
func (h *SupplierPayoutHandler) adminActor(c *gin.Context, stepUp bool) (int64, bool) {
	role, ok := middleware.GetUserRoleFromContext(c)
	if c.GetString("auth_method") != service.AuditAuthMethodJWT || !ok || role != service.RoleAdmin {
		response.Forbidden(c, "Admin JWT required")
		return 0, false
	}
	return h.actor(c, stepUp)
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
	actor, ok := h.adminActor(c, true)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	var req struct {
		Evidence string `json:"evidence" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Verification evidence required")
		return
	}
	v, err := h.payouts.VerifyDestination(c.Request.Context(), id, actor, c.GetHeader("Idempotency-Key"), req.Evidence)
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
	var actor int64
	var ok bool
	if name == "cancel" {
		actor, ok = h.actor(c, true)
	} else {
		actor, ok = h.adminActor(c, true)
	}
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
	actor, ok := h.adminActor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.ListAdmin(c.Request.Context(), actor)
	payoutResponse(c, v, err)
}

// AdminListDestinations must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) AdminListDestinations(c *gin.Context) {
	actor, ok := h.adminActor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.AdminDestinations(c.Request.Context(), actor)
	payoutResponse(c, v, err)
}

// RevealDestination must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) RevealDestination(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	actor, ok := h.adminActor(c, true)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Reason required")
		return
	}
	secret, err := h.payouts.RevealDestination(c.Request.Context(), id, actor, req.Reason, c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.BadRequest(c, "Destination reveal could not be completed")
		return
	}
	response.Success(c, gin.H{"credential": string(secret)})
}

// AdminGet must be registered under the admin-only JWT route group.
func (h *SupplierPayoutHandler) AdminGet(c *gin.Context) {
	actor, ok := h.adminActor(c, false)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	v, err := h.payouts.Get(c.Request.Context(), id, actor, true)
	payoutResponse(c, v, err)
}

func (h *SupplierPayoutHandler) operation(c *gin.Context, kind string) {
	actor, ok := h.actor(c, false)
	if !ok {
		return
	}
	v, err := h.payouts.Operation(c.Request.Context(), actor, kind, c.Param("key"))
	payoutResponse(c, v, err)
}
func (h *SupplierPayoutHandler) PayoutOperation(c *gin.Context)      { h.operation(c, "request") }
func (h *SupplierPayoutHandler) DestinationOperation(c *gin.Context) { h.operation(c, "destination") }

// Freeze requires an administrator JWT and a fresh step-up grant.
func (h *SupplierPayoutHandler) Freeze(c *gin.Context) {
	actor, ok := h.adminActor(c, true)
	if !ok {
		return
	}
	id, ok := payoutID(c)
	if !ok {
		return
	}
	var req struct {
		ExpectedVersion *int64 `json:"expected_version"`
		Frozen          *bool  `json:"frozen"`
		Reason          string `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil || req.ExpectedVersion == nil || req.Frozen == nil {
		response.BadRequest(c, "Invalid freeze request")
		return
	}
	v, err := h.payouts.SetFrozen(c.Request.Context(), service.PayoutFreezeCommand{SupplierID: id, ActorID: actor, ExpectedVersion: *req.ExpectedVersion, Frozen: *req.Frozen, Reason: req.Reason, OperationID: c.GetHeader("Idempotency-Key")})
	payoutResponse(c, v, err)
}

package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"io"
	"strconv"
)

type SupplierAccountHandler struct {
	svc *service.SupplierAccountService
}

func NewSupplierAccountHandler(svc *service.SupplierAccountService) *SupplierAccountHandler {
	return &SupplierAccountHandler{svc: svc}
}
func supplierAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.NotFound(c, "Account not found")
		return 0, false
	}
	return id, true
}
func (h *SupplierAccountHandler) List(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	v, e := h.svc.List(c.Request.Context(), user, service.SupplierAccountFilter{Query: c.Query("q"), Platform: c.Query("platform"), Status: c.Query("status"), Model: c.Query("model"), Page: page, PageSize: size})
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierAccountHandler) Get(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierAccountID(c)
	if !ok {
		return
	}
	v, e := h.svc.Get(c.Request.Context(), user, id)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierAccountHandler) Create(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	var in service.SupplierAccountCreate
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "Invalid account")
		return
	}
	v, e := h.svc.Create(c.Request.Context(), user, c.GetHeader("Idempotency-Key"), in)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierAccountHandler) Patch(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierAccountID(c)
	if !ok {
		return
	}
	var in service.SupplierAccountPatch
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "Invalid account update")
		return
	}
	v, e := h.svc.Patch(c.Request.Context(), user, id, in)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierAccountHandler) Action(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := supplierSubject(c)
		if !ok {
			return
		}
		id, ok := supplierAccountID(c)
		if !ok {
			return
		}
		var in struct {
			ExpectedVersion       int64 `json:"expected_version"`
			ExpectedPolicyVersion int64 `json:"expected_policy_version"`
		}
		if c.ShouldBindJSON(&in) != nil {
			response.BadRequest(c, "Invalid action")
			return
		}
		v, e := h.svc.Transition(c.Request.Context(), user, id, in.ExpectedVersion, action, in.ExpectedPolicyVersion)
		if response.ErrorFrom(c, e) {
			return
		}
		response.Success(c, v)
	}
}
func (h *SupplierAccountHandler) Operation(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	v, e := h.svc.Operation(c.Request.Context(), user, c.Param("key"))
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}

// RegisterSupplierAccountRoutes replaces the basic GET account routes and adds lifecycle routes.
func RegisterSupplierAccountRoutes(supplier *gin.RouterGroup, h *SupplierAccountHandler) {
	supplier.GET("/overview", h.Overview)
	supplier.GET("/accounts", h.List)
	supplier.GET("/accounts/:id", h.Get)
	supplier.POST("/accounts", h.Create)
	supplier.PATCH("/accounts/:id", h.Patch)
	supplier.POST("/accounts/:id/credentials", h.ReplaceCredentials)
	supplier.POST("/accounts/:id/probe", h.Probe)
	for _, a := range []string{"submit", "pause", "resume", "revoke"} {
		supplier.POST("/accounts/:id/"+a, h.Action(a))
	}
	supplier.GET("/operations/:key", h.Operation)
}

func (h *SupplierAccountHandler) AdminReview(c *gin.Context) {
	admin, ok := supplierAdmin(c)
	if !ok {
		return
	}
	id, ok := supplierAccountID(c)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion  int64  `json:"expected_version"`
		Status           string `json:"status"`
		Reason           string `json:"reason"`
		IdentityReviewed bool   `json:"identity_reviewed"`
	}
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "Invalid review")
		return
	}
	v, e := h.svc.AdminReview(c.Request.Context(), admin, id, in.ExpectedVersion, in.Status, in.Reason, in.IdentityReviewed)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func RegisterAdminSupplierOfferRoutes(admin *gin.RouterGroup, h *SupplierAccountHandler) {
	admin.POST("/marketplace/offers/:id/review", h.AdminReview)
}

func (h *SupplierAccountHandler) ReplaceCredentials(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierAccountID(c)
	if !ok {
		return
	}
	var in struct {
		APIKey          string `json:"api_key"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "Invalid credential")
		return
	}
	v, e := h.svc.ReplaceCredentials(c.Request.Context(), user, id, c.GetHeader("Idempotency-Key"), in.APIKey, in.ExpectedVersion)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}

func (h *SupplierAccountHandler) Overview(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	v, e := h.svc.Overview(c.Request.Context(), user)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}

func (h *SupplierAccountHandler) Probe(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierAccountID(c)
	if !ok {
		return
	}
	var raw []byte
	var readErr error
	if c.Request.Body != nil {
		raw, readErr = io.ReadAll(io.LimitReader(c.Request.Body, 1))
	}
	if readErr != nil || len(raw) > 0 {
		response.BadRequest(c, "Probe does not accept a request body")
		return
	}
	v, e := h.svc.Probe(c.Request.Context(), user, id)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}

func (h *SupplierAccountHandler) AdminListOffers(c *gin.Context) {
	admin, ok := supplierAdmin(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	v, err := h.svc.AdminListOffers(c.Request.Context(), admin, service.SupplierAccountFilter{Query: c.Query("q"), Platform: c.Query("platform"), Status: c.Query("status"), Page: page, PageSize: size})
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, v)
}

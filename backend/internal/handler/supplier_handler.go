package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierHandler struct{ svc *service.SupplierService }

func NewSupplierHandler(svc *service.SupplierService) *SupplierHandler {
	return &SupplierHandler{svc: svc}
}
func supplierSubject(c *gin.Context) (int64, bool) {
	s, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || s.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return s.UserID, true
}
func (h *SupplierHandler) Capabilities(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	v, e := h.svc.Capabilities(c.Request.Context(), u)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierHandler) Profile(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	v, e := h.svc.Profile(c.Request.Context(), u)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierHandler) Apply(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		AgreementVersion  int64             `json:"agreement_version"`
		ApplicationFields map[string]string `json:"fields"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid application")
		return
	}
	v, e := h.svc.Apply(c.Request.Context(), u, req.AgreementVersion, req.ApplicationFields)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierHandler) Accounts(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	v, e := h.svc.ListAccounts(c.Request.Context(), u)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierHandler) Account(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.NotFound(c, "Account not found")
		return
	}
	v, e := h.svc.GetAccount(c.Request.Context(), u, id)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}
func (h *SupplierHandler) AdminList(c *gin.Context) {
	if _, ok := supplierAdmin(c); !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	v, total, e := h.svc.ListProfiles(c.Request.Context(), page, pageSize)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Paginated(c, v, total, page, pageSize)
}
func supplierAdmin(c *gin.Context) (int64, bool) {
	u, ok := supplierSubject(c)
	if !ok {
		return 0, false
	}
	role, _ := middleware.GetUserRoleFromContext(c)
	method, _ := c.Get("auth_method")
	if role != "admin" || method != "jwt" || adminaccess.FromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "Administrator required")
		return 0, false
	}
	return u, true
}
func (h *SupplierHandler) AdminReview(c *gin.Context) {
	admin, ok := supplierAdmin(c)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.NotFound(c, "Supplier not found")
		return
	}
	var req struct {
		Status service.SupplierStatus `json:"status"`
		Reason string                 `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid review")
		return
	}
	v, e := h.svc.Review(c.Request.Context(), id, admin, req.Status, req.Reason)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, v)
}

func (h *SupplierHandler) Options(c *gin.Context) {
	if _, ok := supplierSubject(c); !ok {
		return
	}
	response.Success(c, h.svc.Options())
}

package handler

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierFinanceRecoveryResolver interface {
	Resolve(context.Context, int64, string, string, string, string) error
}
type SupplierFinanceHandler struct {
	finance  *service.SupplierFinanceService
	totp     *service.TotpService
	users    *service.UserService
	recovery SupplierFinanceRecoveryResolver
}

func NewSupplierFinanceHandler(finance *service.SupplierFinanceService, totp *service.TotpService, users *service.UserService) *SupplierFinanceHandler {
	return &SupplierFinanceHandler{finance: finance, totp: totp, users: users}
}
func (h *SupplierFinanceHandler) SetRecoveryResolver(r SupplierFinanceRecoveryResolver) {
	h.recovery = r
}
func (h *SupplierFinanceHandler) actor(c *gin.Context, admin, stepUp bool) (int64, bool) {
	if admin {
		role, ok := middleware.GetUserRoleFromContext(c)
		if !ok || role != service.RoleAdmin || c.GetString("auth_method") != service.AuditAuthMethodJWT {
			response.Forbidden(c, "Admin JWT required")
			return 0, false
		}
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
func financeError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, service.ErrSupplierFinanceNotFound):
		response.NotFound(c, "Supplier finance record not found")
	case errors.Is(err, service.ErrSupplierFinanceConflict):
		response.Error(c, http.StatusConflict, "Idempotency key conflicts with another request")
	case errors.Is(err, service.ErrSupplierFinanceInvalid):
		response.BadRequest(c, "Invalid finance request")
	default:
		response.InternalError(c, "Finance request could not be completed")
	}
}
func financeID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return 0, false
	}
	return id, true
}
func financeDate(value string, end bool) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, err
	}
	if end {
		d = d.AddDate(0, 0, 1)
	}
	return &d, nil
}
func (h *SupplierFinanceHandler) Earnings(c *gin.Context) {
	id, ok := h.actor(c, false, false)
	if !ok {
		return
	}
	p, n := response.ParsePagination(c)
	start, e1 := financeDate(c.Query("start_date"), false)
	end, e2 := financeDate(c.Query("end_date"), true)
	var account int64
	var e3 error
	if c.Query("account_id") != "" {
		account, e3 = strconv.ParseInt(c.Query("account_id"), 10, 64)
	}
	if e1 != nil || e2 != nil || e3 != nil || account < 0 || start != nil && end != nil && !start.Before(*end) {
		response.BadRequest(c, "Invalid earnings filter")
		return
	}
	v, err := h.finance.Earnings(c.Request.Context(), id, service.FinanceFilter{Page: p, PageSize: n, Start: start, End: end, AccountID: account, Model: c.Query("model")})
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) Statements(c *gin.Context) {
	id, ok := h.actor(c, false, false)
	if !ok {
		return
	}
	p, n := response.ParsePagination(c)
	v, err := h.finance.Statements(c.Request.Context(), id, p, n)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) Statement(c *gin.Context) {
	owner, ok := h.actor(c, false, false)
	if !ok {
		return
	}
	id, ok := financeID(c)
	if !ok {
		return
	}
	v, err := h.finance.Statement(c.Request.Context(), owner, id)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) StatementEntries(c *gin.Context) {
	owner, ok := h.actor(c, false, false)
	if !ok {
		return
	}
	id, ok := financeID(c)
	if !ok {
		return
	}
	p, n := response.ParsePagination(c)
	v, err := h.finance.StatementEntries(c.Request.Context(), owner, id, p, n)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) ExportStatement(c *gin.Context) {
	owner, ok := h.actor(c, false, false)
	if !ok {
		return
	}
	id, ok := financeID(c)
	if !ok {
		return
	}
	v, err := h.finance.Statement(c.Request.Context(), owner, id)
	if err != nil {
		financeError(c, err)
		return
	}
	entries, err := h.finance.StatementEntries(c.Request.Context(), owner, id, 1, 100)
	if err != nil {
		financeError(c, err)
		return
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"month", "currency", "version", "opening_pending", "opening_available", "opening_reserved", "closing_pending", "closing_available", "closing_reserved", "earned_net", "platform_fee", "refund_net", "payout_paid", "cutoff_ledger_id"})
	fields := []string{v.Month, v.Currency, strconv.Itoa(v.Version), v.OpeningPending, v.OpeningAvailable, v.OpeningReserved, v.ClosingPending, v.ClosingAvailable, v.ClosingReserved, v.EarnedNet, v.PlatformFee, v.RefundNet, v.PayoutPaid, strconv.FormatInt(v.CutoffLedgerID, 10)}
	for i := range fields {
		fields[i] = service.SupplierCSVCell(fields[i])
	}
	_ = w.Write(fields)
	_ = w.Write([]string{})
	_ = w.Write([]string{"operation_id", "date", "account_id", "account_name", "model", "kind", "consumer_charge", "platform_fee", "supplier_net"})
	for page := 1; ; page++ {
		for _, e := range entries.Items {
			row := []string{e.OperationID, e.Date.UTC().Format(time.RFC3339Nano), strconv.FormatInt(e.AccountID, 10), e.AccountName, e.Model, e.Kind, e.ConsumerCharge, e.PlatformFee, e.SupplierNet}
			for i := range row {
				row[i] = service.SupplierCSVCell(row[i])
			}
			_ = w.Write(row)
		}
		if page >= entries.Pages {
			break
		}
		entries, err = h.finance.StatementEntries(c.Request.Context(), owner, id, page+1, 100)
		if err != nil {
			financeError(c, err)
			return
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		financeError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="supplier-statement-`+v.Month+`.csv"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}
func (h *SupplierFinanceHandler) AdminReconciliation(c *gin.Context) {
	_, ok := h.actor(c, true, false)
	if !ok {
		return
	}
	p, n := response.ParsePagination(c)
	v, err := h.finance.Reconciliation(c.Request.Context(), c.Query("status"), p, n)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) AdminGenerateStatement(c *gin.Context) {
	actor, ok := h.actor(c, true, true)
	if !ok {
		return
	}
	var req struct {
		SupplierID int64  `json:"supplier_id"`
		Month      string `json:"month"`
		Reason     string `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid generation request")
		return
	}
	v, err := h.finance.AdminGenerateClosedMonth(c.Request.Context(), actor, req.SupplierID, req.Month, c.GetHeader("Idempotency-Key"), req.Reason)
	if err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *SupplierFinanceHandler) AdminResolve(c *gin.Context) {
	actor, ok := h.actor(c, true, true)
	if !ok {
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	operation := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Reason) == "" || operation == "" || len(operation) > 255 || (req.Action != "release_confirmed_unexecuted" && req.Action != "retry_known_usage") || strings.TrimSpace(c.Param("id")) == "" {
		response.BadRequest(c, "Invalid recovery request")
		return
	}
	if h.recovery == nil {
		response.Error(c, http.StatusServiceUnavailable, "Recovery service unavailable")
		return
	}
	if err := h.recovery.Resolve(c.Request.Context(), actor, c.Param("id"), req.Action, req.Reason, operation); err != nil {
		financeError(c, err)
		return
	}
	response.Success(c, gin.H{"operation_id": operation, "status": "confirmed"})
}

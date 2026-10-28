package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fundingAdminHandlerFake struct {
	manualChargebackTestService
	cmd     service.FundingReclassCommand
	calls   int
	failure error
}

func (s *fundingAdminHandlerFake) ReclassifyLegacyFunding(_ context.Context, c service.FundingReclassCommand) (*service.FundingReclassResult, error) {
	s.calls++
	s.cmd = c
	if s.failure != nil {
		return nil, s.failure
	}
	if c.OperationID == "" {
		return nil, errors.New("missing key")
	}
	return &service.FundingReclassResult{UserID: c.UserID, OrderID: c.OrderID, Amount: c.Amount, OperationID: c.OperationID}, nil
}
func (*fundingAdminHandlerFake) ListFundingRisks(context.Context, int64) ([]service.FundingRisk, error) {
	return []service.FundingRisk{}, nil
}
func (s *fundingAdminHandlerFake) ClearFundingRisk(context.Context, service.FundingRiskClearCommand) (*service.FundingRiskClearResult, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return &service.FundingRiskClearResult{}, nil
}
func TestFundingAdminReclassifyRequiresAdminTotpAndExactAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fundingAdminHandlerFake{}
	request := func(role, method string, grant bool, body string) *httptest.ResponseRecorder {
		users := manualChargebackTestUsers{role: role, status: "active"}
		h := NewPaymentFundingRecoveryHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
			c.Set("auth_method", method)
			c.Next()
		})
		r.POST("/reclassify", h.Reclassify)
		w := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodPost, "/reclassify", strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Idempotency-Key", "operation-1")
		r.ServeHTTP(w, q)
		return w
	}
	good := `{"user_id":10,"order_id":20,"amount":"1.12345678","evidence":"bank statement","remaining_provenance_confirmed":true}`
	for _, tc := range []struct {
		role, method string
		grant        bool
	}{{"user", "jwt", true}, {"admin", "admin_api_key", true}, {"admin", "jwt", false}} {
		require.Equal(t, http.StatusForbidden, request(tc.role, tc.method, tc.grant, good).Code)
	}
	require.Zero(t, store.calls)
	for _, body := range []string{`{"user_id":10,"order_id":20,"amount":"1","evidence":"paid only"}`, `{"user_id":10,"order_id":20,"amount":"1","evidence":"paid only","remaining_provenance_confirmed":false}`, `{"user_id":10,"order_id":20,"amount":1.2}`, `{"user_id":10,"order_id":20,"amount":"1.000000001"}`, `{"user_id":10,"order_id":20,"amount":"1e2"}`} {
		require.Equal(t, http.StatusBadRequest, request("admin", "jwt", true, body).Code)
	}
	require.Zero(t, store.calls)
	require.Equal(t, http.StatusOK, request("admin", "jwt", true, good).Code)
	require.Equal(t, int64(7), store.cmd.ActorID)
	require.Equal(t, "operation-1", store.cmd.OperationID)
	require.Equal(t, "1.12345678", store.cmd.Amount.String())
	require.True(t, store.cmd.RemainingProvenanceConfirmed)
}

func TestFundingAdminErrorsDistinguishNoEffectFromUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, body string
		failure          error
		status           int
		reason           string
	}{
		{"reclass-rejected", "/reclassify", `{"user_id":10,"order_id":20,"amount":"1","evidence":"verified","remaining_provenance_confirmed":true}`, service.ErrInvalidFunding, 400, "FUNDING_OPERATION_REJECTED"},
		{"stale", "/risk/10", `{"expected_version":2,"evidence":"verified"}`, service.ErrFundingRiskStale, 409, "FUNDING_RISK_STALE"},
		{"conflict", "/risk/10", `{"expected_version":2,"evidence":"verified"}`, service.ErrFundingAdminConflict, 409, "FUNDING_OPERATION_CONFLICT"},
		{"commit-unknown", "/risk/10", `{"expected_version":2,"evidence":"verified"}`, errors.New("commit failed"), 503, "FUNDING_OPERATION_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := manualChargebackTestUsers{role: "admin", status: "active"}
			store := &fundingAdminHandlerFake{failure: tc.failure}
			h := NewPaymentFundingRecoveryHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set(string(middleware.ContextKeyUserRole), "admin")
				c.Set("auth_method", "jwt")
				c.Next()
			})
			r.POST("/reclassify", h.Reclassify)
			r.POST("/risk/:id", h.ClearRisk)
			q := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			q.Header.Set("Content-Type", "application/json")
			q.Header.Set("Idempotency-Key", "op")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			require.Equal(t, tc.status, w.Code)
			require.Contains(t, w.Body.String(), tc.reason)
		})
	}
}

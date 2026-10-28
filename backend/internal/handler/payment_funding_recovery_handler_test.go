package handler

import (
	"context"
	"errors"
	"strings"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFundingRecoveryFailsClosedWithoutAdminSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewPaymentFundingRecoveryHandler(nil, nil, nil)
	r.GET("/review", h.List)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/review", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", w.Code)
	}
}

type manualChargebackTestUsers struct {
	service.UserRepository
	role, status string
}

func (u manualChargebackTestUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Role: u.role, Status: u.status, TotpEnabled: true}, nil
}
func (manualChargebackTestUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type manualChargebackTestService struct {
	order, actor    int64
	amount          decimal.Decimal
	event, evidence string
	calls           int
}

func (*manualChargebackTestService) ListPendingFundingRefunds(context.Context, int) ([]service.FundingRefundReviewItem, error) {
	return nil, nil
}
func (*manualChargebackTestService) ResolveUncertainFunding(context.Context, service.UncertainFundingResolution) (*service.UncertainFundingResolutionResult, error) {
	return nil, nil
}
func (s *manualChargebackTestService) RecordExternalChargebackExact(_ context.Context, order, actor int64, amount decimal.Decimal, event, evidence string) error {
	s.calls++
	if s.event != "" && (s.order != order || s.actor != actor || !s.amount.Equal(amount) || s.event != event || s.evidence != evidence) {
		return errors.New("conflicting event")
	}
	s.order, s.actor, s.amount, s.event, s.evidence = order, actor, amount, event, evidence
	return nil
}
func TestManualChargebackRequiresActiveAdminStepUpAndExactAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &manualChargebackTestService{}
	request := func(role, method, status string, grant bool, body string) *httptest.ResponseRecorder {
		users := manualChargebackTestUsers{role: role, status: status}
		h := NewPaymentFundingRecoveryHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
			c.Set("auth_method", method)
			c.Next()
		})
		r.POST("/chargebacks", h.Chargeback)
		w := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodPost, "/chargebacks", strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, q)
		return w
	}
	good := `{"order_id":123,"credited_amount":"123456789.12345678","provider_event_id":"evt-1","evidence":"bank statement"}`
	for _, tc := range []struct {
		role, method, status string
		grant                bool
	}{{"user", "jwt", "active", true}, {"admin", "admin_api_key", "active", true}, {"admin", "jwt", "disabled", true}, {"admin", "jwt", "active", false}} {
		require.Equal(t, http.StatusForbidden, request(tc.role, tc.method, tc.status, tc.grant, good).Code)
	}
	require.Zero(t, store.calls)
	for _, body := range []string{
		`{"order_id":1,"amount":"1","provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":0,"credited_amount":"1","provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":1.5,"provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":"0","provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":"1.000000001","provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":"1e2","provider_event_id":"evt","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":"1","provider_event_id":" ","evidence":"proof"}`,
		`{"order_id":1,"credited_amount":"1","provider_event_id":"evt","evidence":" "}`,
	} {
		require.Equal(t, http.StatusBadRequest, request("admin", "jwt", "active", true, body).Code, body)
	}
	require.Zero(t, store.calls)
	require.Equal(t, http.StatusOK, request("admin", "jwt", "active", true, good).Code)
	require.Equal(t, int64(123), store.order)
	require.Equal(t, int64(7), store.actor)
	require.True(t, store.amount.Equal(decimal.RequireFromString("123456789.12345678")))
	require.Equal(t, "evt-1", store.event)
	require.Equal(t, "bank statement", store.evidence)
	require.Equal(t, http.StatusOK, request("admin", "jwt", "active", true, good).Code)
	require.Equal(t, http.StatusBadRequest, request("admin", "jwt", "active", true, strings.Replace(good, "123456789.12345678", "1.00", 1)).Code)
}

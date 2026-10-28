//go:build unit

package handler

import (
	"context"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type refundTestStore struct {
	actor              int64
	receipt, operation string
	amount             decimal.Decimal
	reason             string
}

func (s *refundTestStore) Refund(_ context.Context, r string, a int64, n decimal.Decimal, o string) error {
	s.actor = a
	s.receipt = r
	s.operation = o
	s.amount = n
	return nil
}
func (s *refundTestStore) RefundWithReason(ctx context.Context, r string, a int64, n decimal.Decimal, o, reason string) error {
	s.reason = reason
	return s.Refund(ctx, r, a, n, o)
}
func TestMarketplaceRefundHandlerRequiresAdminAndExactMoney(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &refundTestStore{}
	h := NewMarketplaceRefundHandler(service.NewMarketplaceRefundService(store))
	users := payoutTestUsers{}
	h.SetStepUpDeps(service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	makeRequest := func(role, method, body string) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 4})
			c.Set(string(middleware.ContextKeyUserRole), role)
			c.Set("auth_method", method)
			c.Next()
		})
		r.POST("/:id/refund", h.Refund)
		w := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodPost, "/01b00475-399a-4f93-8445-6d6d76a4ff67/refund", strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Idempotency-Key", "refund-key")
		r.ServeHTTP(w, q)
		return w
	}
	require.Equal(t, 403, makeRequest("user", "jwt", `{"amount":"1.00000001","reason":"billing correction"}`).Code)
	require.Equal(t, 403, makeRequest("admin", "admin_api_key", `{"amount":"1.00000001","reason":"billing correction"}`).Code)
	require.Equal(t, 400, makeRequest("admin", "jwt", `{"amount":1.5}`).Code)
	require.Equal(t, 200, makeRequest("admin", "jwt", `{"amount":"1.00000001","reason":"billing correction"}`).Code)
	require.Equal(t, int64(4), store.actor)
	require.Equal(t, "refund-key", store.operation)
	require.Equal(t, "billing correction", store.reason)
	require.True(t, store.amount.Equal(decimal.RequireFromString("1.00000001")))
}

func TestMarketplaceRefundHandlerRequiresStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &refundTestStore{}
	h := NewMarketplaceRefundHandler(service.NewMarketplaceRefundService(store))
	users := payoutTestUsers{}
	h.SetStepUpDeps(service.NewTotpService(users, nil, payoutTestGrant{granted: false}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 4})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", "jwt")
		c.Next()
	})
	r.POST("/:id/refund", h.Refund)
	q := httptest.NewRequest(http.MethodPost, "/01b00475-399a-4f93-8445-6d6d76a4ff67/refund", strings.NewReader(`{"amount":"1","reason":"correction"}`))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", "refund")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	require.Equal(t, 403, w.Code)
	require.Zero(t, store.actor)
}

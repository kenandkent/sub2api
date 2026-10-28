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
}

func (s *refundTestStore) Refund(_ context.Context, r string, a int64, n decimal.Decimal, o string) error {
	s.actor = a
	s.receipt = r
	s.operation = o
	s.amount = n
	return nil
}
func TestMarketplaceRefundHandlerRequiresAdminAndExactMoney(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &refundTestStore{}
	h := NewMarketplaceRefundHandler(service.NewMarketplaceRefundService(store))
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
	require.Equal(t, 403, makeRequest("user", "jwt", `{"amount":"1.00000001"}`).Code)
	require.Equal(t, 403, makeRequest("admin", "admin_api_key", `{"amount":"1.00000001"}`).Code)
	require.Equal(t, 400, makeRequest("admin", "jwt", `{"amount":1.5}`).Code)
	require.Equal(t, 200, makeRequest("admin", "jwt", `{"amount":"1.00000001"}`).Code)
	require.Equal(t, int64(4), store.actor)
	require.Equal(t, "refund-key", store.operation)
	require.True(t, store.amount.Equal(decimal.RequireFromString("1.00000001")))
}

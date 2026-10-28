package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminReceiptTestStore struct {
	service.SupplierFinanceStore
	calls int
}

func (s *adminReceiptTestStore) AdminReceipt(_ context.Context, actor int64, id string) (*service.MarketplaceAdminReceipt, error) {
	s.calls++
	return &service.MarketplaceAdminReceipt{ReceiptID: id, Captured: true, ActualCharge: "3", RefundableAmount: "2"}, nil
}
func TestAdminReceiptRequiresAdminJWTAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, role, auth string
		grant            bool
		status           int
	}{
		{"allowed", service.RoleAdmin, "jwt", true, http.StatusOK},
		{"no step up", service.RoleAdmin, "jwt", false, http.StatusForbidden},
		{"api key", service.RoleAdmin, service.AuditAuthMethodAdminAPIKey, true, http.StatusForbidden},
		{"nonadmin", service.RoleUser, "jwt", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &adminReceiptTestStore{}
			users := payoutTestUsers{}
			h := NewSupplierFinanceHandler(service.NewSupplierFinanceService(store), service.NewTotpService(users, nil, payoutTestGrant{granted: tc.grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
				c.Set("auth_method", tc.auth)
				c.Next()
			})
			r.GET("/receipts/:id", h.AdminReceipt)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/receipts/84ee869e-456c-4afe-a044-1306747d6c73", nil))
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if tc.status == http.StatusOK {
				require.Equal(t, 1, store.calls)
				require.Contains(t, w.Body.String(), `"refundable_amount":"2"`)
			} else {
				require.Zero(t, store.calls)
			}
		})
	}
}

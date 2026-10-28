package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceRepairRoutesRequireAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Supplier: &handler.SupplierHandler{}, Marketplace: &handler.MarketplaceHandlers{
		Accounts: &handler.SupplierAccountHandler{}, Payouts: &handler.SupplierPayoutHandler{},
		PaymentFundingRecovery: handler.NewPaymentFundingRecoveryHandler(nil, nil, nil),
		Catalog:                &handler.MarketplaceCatalogHandler{}, Finance: &handler.SupplierFinanceHandler{}, Refunds: &handler.MarketplaceRefundHandler{},
		Vault: handler.NewMarketplaceVaultRotationHandler(nil, nil, nil),
	}}
	registerAdminSupplierRoutes(router.Group("/api/v1/admin"), h)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/funding-reclassifications"},
		{http.MethodGet, "/funding-risks"},
		{http.MethodPost, "/funding-risks/1/clear"},
		{http.MethodGet, "/receipts/12345678-1234-4234-9234-123456789abc"},
		{http.MethodPost, "/vault/rotate"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/v1/admin/marketplace"+tc.path, nil))
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
		})
	}
}

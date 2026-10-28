package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExecutionIncidentRoutesAreAdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{Supplier: &handler.SupplierHandler{}, Marketplace: &handler.MarketplaceHandlers{Accounts: &handler.SupplierAccountHandler{}, Payouts: &handler.SupplierPayoutHandler{}, PaymentFundingRecovery: handler.NewPaymentFundingRecoveryHandler(nil, nil, nil), Catalog: &handler.MarketplaceCatalogHandler{}, Finance: &handler.SupplierFinanceHandler{}, Refunds: &handler.MarketplaceRefundHandler{}}}
	registerAdminSupplierRoutes(r.Group("/api/v1/admin"), h)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/admin/marketplace/receipts/4b112fb9-26c6-4a10-bfba-8e247a51b1b4/execution-incidents", nil))
		require.Equal(t, http.StatusForbidden, w.Code)
	}
}

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestManualChargebackRegisteredUnderAdminAndRejectsAnonymous(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Supplier: &handler.SupplierHandler{}, Marketplace: &handler.MarketplaceHandlers{
		Accounts: &handler.SupplierAccountHandler{}, Payouts: &handler.SupplierPayoutHandler{},
		PaymentFundingRecovery: handler.NewPaymentFundingRecoveryHandler(nil, nil, nil),
		Catalog:                &handler.MarketplaceCatalogHandler{}, Finance: &handler.SupplierFinanceHandler{}, Refunds: &handler.MarketplaceRefundHandler{},
	}}
	registerAdminSupplierRoutes(router.Group("/api/v1/admin"), h)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/marketplace/funding-chargebacks", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/funding-chargebacks", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

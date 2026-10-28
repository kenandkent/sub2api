package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerSupplierRoutes(authenticated *gin.RouterGroup, h *handler.Handlers) {
	authenticated.GET("/user/capabilities", h.Supplier.Capabilities)
	supplier := authenticated.Group("/supplier")
	supplier.GET("/options", h.Supplier.Options)
	supplier.GET("/profile", h.Supplier.Profile)
	supplier.POST("/profile", h.Supplier.Apply)
	if h.Marketplace == nil {
		supplier.GET("/accounts", h.Supplier.Accounts)
		supplier.GET("/accounts/:id", h.Supplier.Account)
		return
	}
	m := h.Marketplace
	handler.RegisterSupplierAccountRoutes(supplier, m.Accounts)
	supplier.POST("/authorizations", m.Authorization.Start)
	supplier.GET("/authorizations/:id", m.Authorization.Status)
	supplier.POST("/authorizations/:id/complete", m.Authorization.Complete)
	supplier.GET("/earnings", m.Finance.Earnings)
	supplier.GET("/statements", m.Finance.Statements)
	supplier.GET("/statements/:id", m.Finance.Statement)
	supplier.GET("/statements/:id/entries", m.Finance.StatementEntries)
	supplier.GET("/statements/:id/export.csv", m.Finance.ExportStatement)
	supplier.GET("/payout-destinations", m.Payouts.ListDestinations)
	supplier.POST("/payout-destinations", m.Payouts.CreateDestination)
	supplier.GET("/payouts", m.Payouts.List)
	supplier.POST("/payouts", m.Payouts.Request)
	supplier.GET("/payouts/:id", m.Payouts.Get)
	supplier.POST("/payouts/:id/cancel", m.Payouts.Cancel)
	supplier.GET("/payout-operations/:key", m.Payouts.PayoutOperation)
	supplier.GET("/payout-destination-operations/:key", m.Payouts.DestinationOperation)
	authenticated.GET("/marketplace/catalog", m.Catalog.Catalog)
	authenticated.GET("/marketplace/balance", m.Catalog.Balance)
}
func registerAdminSupplierRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	suppliers := admin.Group("/marketplace/suppliers")
	suppliers.GET("", h.Supplier.AdminList)
	suppliers.POST("/:id/review", h.Supplier.AdminReview)
	if h.Marketplace == nil {
		return
	}
	m := h.Marketplace
	handler.RegisterAdminSupplierOfferRoutes(admin, m.Accounts)
	marketplace := admin.Group("/marketplace")
	marketplace.GET("/offers", m.Accounts.AdminListOffers)
	marketplace.POST("/suppliers/:id/payout-freeze", m.Payouts.Freeze)
	marketplace.GET("/funding-refunds", m.PaymentFundingRecovery.List)
	marketplace.POST("/funding-refunds/resolve", m.PaymentFundingRecovery.Resolve)
	marketplace.POST("/funding-chargebacks", m.PaymentFundingRecovery.Chargeback)
	marketplace.POST("/funding-reclassifications", m.PaymentFundingRecovery.Reclassify)
	marketplace.GET("/funding-risks", m.PaymentFundingRecovery.Risks)
	marketplace.POST("/funding-risks/:id/clear", m.PaymentFundingRecovery.ClearRisk)
	if m.Vault != nil {
		marketplace.POST("/vault/rotate", m.Vault.Rotate)
	}
	marketplace.GET("/policies", m.Catalog.AdminCurrent)
	marketplace.POST("/policies", m.Catalog.Publish)
	marketplace.POST("/wallets/:id/migrate", m.Catalog.MigrateWallet)
	marketplace.GET("/reconciliation", m.Finance.AdminReconciliation)
	marketplace.POST("/statements/generate", m.Finance.AdminGenerateStatement)
	marketplace.GET("/receipts/:id", m.Finance.AdminReceipt)
	marketplace.POST("/receipts/:id/reconcile", m.Finance.AdminResolve)
	marketplace.POST("/receipts/:id/execution-incidents", m.Finance.AdminRegisterExecutionIncident)
	marketplace.GET("/receipts/:id/execution-incidents", m.Finance.AdminListExecutionIncidents)
	marketplace.POST("/receipts/:id/refund", m.Refunds.Refund)
	marketplace.GET("/payout-destinations", m.Payouts.AdminListDestinations)
	marketplace.POST("/payout-destinations/:id/verify", m.Payouts.VerifyDestination)
	marketplace.POST("/payout-destinations/:id/reveal", m.Payouts.RevealDestination)
	marketplace.GET("/payouts", m.Payouts.AdminList)
	marketplace.GET("/payouts/:id", m.Payouts.AdminGet)
	marketplace.POST("/payouts/:id/approve", m.Payouts.Approve)
	marketplace.POST("/payouts/:id/reject", m.Payouts.Reject)
	marketplace.POST("/payouts/:id/start", m.Payouts.Start)
	marketplace.POST("/payouts/:id/confirm", m.Payouts.Confirm)
	marketplace.POST("/payouts/:id/fail", m.Payouts.Fail)
}

package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// MarketplaceHandlers keeps the opt-in feature's wiring together while its
// history and settlement endpoints remain available when dispatch is paused.
type MarketplaceHandlers struct {
	Supplier               *SupplierHandler
	Accounts               *SupplierAccountHandler
	Authorization          *SupplierAuthorizationHandler
	Finance                *SupplierFinanceHandler
	Payouts                *SupplierPayoutHandler
	Catalog                *MarketplaceCatalogHandler
	Refunds                *MarketplaceRefundHandler
	PaymentFundingRecovery *PaymentFundingRecoveryHandler
	Gateway                *service.MarketplaceGateway
	Stop                   func()
}

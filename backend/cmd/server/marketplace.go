package main

import (
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

func provideMarketplaceHandlers(db *sql.DB, rdb *redis.Client, cfg *config.Config,
	suppliers *service.SupplierService, profiles service.SupplierRepository,
	concurrency *service.ConcurrencyService, oauth service.OpenAIOAuthClient,
	tokenLock service.GeminiTokenCache, keys *service.APIKeyService,
	totp *service.TotpService, users *service.UserService,
	payments *service.PaymentService, billingCache *service.BillingCacheService,
	gatewayService *service.OpenAIGatewayService,
) (*handler.MarketplaceHandlers, error) {
	envelope, err := service.NewConfiguredMarketplaceEnvelope(cfg)
	if err != nil {
		return nil, fmt.Errorf("marketplace credential key configuration is invalid")
	}
	seen := map[string]bool{}
	for _, provider := range cfg.Marketplace.Providers {
		if seen[provider.Platform] {
			return nil, fmt.Errorf("marketplace requires one trusted configuration per platform")
		}
		seen[provider.Platform] = true
	}
	catalog := service.NewMarketplaceCatalogService(repository.NewMarketplaceCatalogRepository(db))
	policy := service.NewMarketplacePolicyService(cfg)
	policy.SetTermsSource(catalog)
	funding := service.NewWalletFundingMigrationService(repository.NewWalletFundingMigrationRepository(db))
	accounts := service.NewSupplierAccountService(cfg, repository.NewSupplierAccountRepository(db, envelope), profiles, catalog, concurrency)
	supplierHandler := handler.NewSupplierHandler(suppliers)
	supplierHandler.SetAccountService(accounts)
	supplierHandler.SetCatalogService(catalog)
	writer := repository.NewSharingOfferRepository(db, envelope)
	authorization, err := service.NewSupplierAuthorizationService(repository.NewSupplierAuthorizationCache(rdb), oauth, writer,
		service.SupplierAuthorizationConfig{Provider: "openai", RedirectURI: openai.DefaultRedirectURI, ProxyURL: cfg.Marketplace.OAuthProxyURL})
	if err != nil {
		return nil, err
	}
	authorization.SetOperationStore(repository.NewSupplierAuthorizationOperationStore(db))
	authorization.SetAuthorizer(accounts)
	providerConfig := service.SupplierRuntimeProviderConfig{ProxyURL: cfg.Marketplace.OAuthProxyURL}
	for _, provider := range cfg.Marketplace.Providers {
		if provider.Platform == service.PlatformOpenAI {
			providerConfig.BaseURL = provider.BaseURL
		}
	}
	runtime := service.NewSupplierRuntimeAccountService(envelope, repository.NewSupplierRuntimeCredentialRepository(db), oauth, tokenLock, providerConfig)
	gatewayStore := repository.NewMarketplaceGatewayRepository(db, cfg)
	financeService := service.NewSupplierFinanceService(repository.NewSupplierFinanceRepository(db))
	finance := handler.NewSupplierFinanceHandler(financeService, totp, users)
	finance.SetRecoveryResolver(gatewayStore)
	refunds := handler.NewMarketplaceRefundHandler(service.NewMarketplaceRefundService(repository.NewMarketplaceRefundRepository(db)))
	refunds.SetStepUpDeps(totp, users)
	background := repository.NewMarketplaceBackgroundWorker(db, billingCache, service.NewSupplierSettlementService(repository.NewSupplierSettlementRepository(db)), financeService)
	background.Start()
	catalogHandler := handler.NewMarketplaceCatalogHandler(catalog, funding, keys, cfg, totp, users)
	catalogHandler.SetMarketplaceRateResolver(gatewayService)
	return &handler.MarketplaceHandlers{
		Supplier:               supplierHandler,
		Accounts:               handler.NewSupplierAccountHandler(accounts),
		Authorization:          handler.NewSupplierAuthorizationHandler(authorization),
		Finance:                finance,
		Payouts:                handler.NewSupplierPayoutHandler(service.NewSupplierPayoutService(repository.NewSupplierPayoutRepository(db), policy, envelope), totp, users),
		Catalog:                catalogHandler,
		Refunds:                refunds,
		PaymentFundingRecovery: handler.NewPaymentFundingRecoveryHandler(payments, totp, users),
		Gateway:                service.NewMarketplaceGateway(catalog, gatewayStore, runtime),
		Stop:                   background.Stop,
	}, nil
}

package handler

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type MarketplaceCatalogHandler struct {
	catalog      *service.MarketplaceCatalogService
	funding      *service.WalletFundingMigrationService
	keys         *service.APIKeyService
	cfg          *config.Config
	totp         *service.TotpService
	users        *service.UserService
	rateResolver interface {
		ResolveUserGroupRateMultiplier(context.Context, int64, int64, float64) float64
	}
}

func NewMarketplaceCatalogHandler(catalog *service.MarketplaceCatalogService, funding *service.WalletFundingMigrationService,
	keys *service.APIKeyService, cfg *config.Config, totp *service.TotpService, users *service.UserService) *MarketplaceCatalogHandler {
	return &MarketplaceCatalogHandler{catalog: catalog, funding: funding, keys: keys, cfg: cfg, totp: totp, users: users}
}

func (h *MarketplaceCatalogHandler) SetMarketplaceRateResolver(resolver interface {
	ResolveUserGroupRateMultiplier(context.Context, int64, int64, float64) float64
}) {
	h.rateResolver = resolver
}

func (h *MarketplaceCatalogHandler) Catalog(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	if h.cfg == nil || !h.cfg.Marketplace.Enabled {
		response.Success(c, gin.H{"version": 0, "groups": []service.MarketplaceCatalogGroup{}})
		return
	}
	catalog, err := h.catalog.Current(c.Request.Context())
	if err != nil {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Published model prices are unavailable", "price_unavailable", nil)
		return
	}
	groups, err := h.keys.GetAvailableGroups(c.Request.Context(), user)
	if response.ErrorFrom(c, err) {
		return
	}
	if h.rateResolver == nil {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Published model prices are unavailable", "price_unavailable", nil)
		return
	}
	allowed := map[int64]service.Group{}
	for _, g := range groups {
		allowed[g.ID] = g
	}
	visible := []service.MarketplaceCatalogGroup{}
	now := time.Now()
	for _, g := range catalog.Book.Groups {
		group, ok := allowed[g.GroupID]
		if !ok {
			continue
		}
		factor := h.rateResolver.ResolveUserGroupRateMultiplier(c.Request.Context(), user, g.GroupID, group.RateMultiplier) * group.PeakMultiplierAt(now)
		if math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 0 {
			response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Published model prices are unavailable", "price_unavailable", nil)
			return
		}
		consumer := decimal.NewFromFloat(factor)
		g.Models = append([]service.MarketplaceCatalogModel(nil), g.Models...)
		for i := range g.Models {
			model := &g.Models[i]
			if model.RateMultiplier == nil {
				response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Published model prices are unavailable", "price_unavailable", nil)
				return
			}
			published := *model.RateMultiplier
			effective := published.Mul(consumer)
			if effective.GreaterThan(decimal.RequireFromString("999999.9999")) {
				response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Published model prices are unavailable", "price_unavailable", nil)
				return
			}
			model.PublishedRateMultiplier, model.ConsumerRateMultiplier, model.RateMultiplier = &published, &consumer, &effective
		}
		visible = append(visible, g)
	}
	response.Success(c, gin.H{"version": catalog.Version, "groups": visible, "currency": "USD", "funding": "cash_paid_or_platform_subsidy"})
}

func (h *MarketplaceCatalogHandler) Balance(c *gin.Context) {
	user, ok := supplierSubject(c)
	if !ok {
		return
	}
	balance, err := h.funding.GetBalance(c.Request.Context(), user)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, balance)
}

func (h *MarketplaceCatalogHandler) AdminCurrent(c *gin.Context) {
	if _, ok := supplierAdmin(c); !ok {
		return
	}
	catalog, err := h.catalog.Current(c.Request.Context())
	if err == service.ErrMarketplaceCatalogUnavailable {
		response.Success(c, gin.H{"version": 0, "book": service.MarketplacePriceBook{Groups: []service.MarketplaceCatalogGroup{}}})
		return
	}
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, catalog)
}

func (h *MarketplaceCatalogHandler) Publish(c *gin.Context) {
	actor, ok := supplierAdmin(c)
	if !ok {
		return
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var req struct {
		Policy struct {
			FeeBPS         *int           `json:"fee_bps"`
			HoldHours      *int           `json:"hold_hours"`
			MaxConcurrency *int           `json:"max_supplier_concurrency"`
			Minimum        *service.Money `json:"minimum_payout_usd"`
		} `json:"policy"`
		Book service.MarketplacePriceBook `json:"book"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Policy.FeeBPS == nil || req.Policy.HoldHours == nil || req.Policy.MaxConcurrency == nil || req.Policy.Minimum == nil {
		response.BadRequest(c, "All policy terms and model prices must be explicit")
		return
	}
	policy := service.MarketplacePolicyVersion{FeeBPS: *req.Policy.FeeBPS, HoldHours: *req.Policy.HoldHours,
		MaxSupplierConcurrency: *req.Policy.MaxConcurrency, MinimumPayoutUSD: *req.Policy.Minimum}
	published, err := h.catalog.Publish(c.Request.Context(), actor, policy, req.Book)
	if err != nil {
		response.BadRequest(c, "Policy publication failed; verify dedicated groups, prices and limits")
		return
	}
	response.Success(c, published)
}

func (h *MarketplaceCatalogHandler) MigrateWallet(c *gin.Context) {
	if _, ok := supplierAdmin(c); !ok {
		return
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user")
		return
	}
	if err = h.funding.MigrateUser(c.Request.Context(), id); err != nil {
		response.Error(c, http.StatusConflict, "Wallet migration needs review; existing frozen funds could not be reconciled")
		return
	}
	balance, err := h.funding.GetBalance(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, balance)
}

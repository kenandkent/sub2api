package service

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var ErrMarketplaceCatalogUnavailable = errors.New("published marketplace catalog unavailable")

type MarketplaceCatalogModel struct {
	PublicModel     string                 `json:"public_model"`
	ActualModel     string                 `json:"actual_model"`
	ContextLimit    int64                  `json:"context_limit"`
	MaxOutputTokens int64                  `json:"max_output_tokens"`
	RateMultiplier *decimal.Decimal       `json:"rate_multiplier"`
	Tiers          []MarketplacePriceTier `json:"tiers"`
}

type MarketplaceCatalogGroup struct {
	GroupID  int64                     `json:"group_id"`
	Name     string                    `json:"name"`
	Platform string                    `json:"platform"`
	Protocol string                    `json:"protocol"`
	Models   []MarketplaceCatalogModel `json:"models"`
}

type MarketplacePriceBook struct {
	Groups []MarketplaceCatalogGroup `json:"groups"`
}

type MarketplacePublishedCatalog struct {
	Version     int64                    `json:"version"`
	PublishedAt time.Time                `json:"published_at"`
	Policy      MarketplacePolicyVersion `json:"policy"`
	Book        MarketplacePriceBook     `json:"book"`
}

type MarketplaceCatalogRepository interface {
	Current(context.Context) (*MarketplacePublishedCatalog, error)
	IsSharedGroup(context.Context, int64) (bool, error)
	Publish(context.Context, int64, MarketplacePolicyVersion, MarketplacePriceBook) (*MarketplacePublishedCatalog, error)
}

type MarketplaceCatalogService struct{ repo MarketplaceCatalogRepository }

func (s *MarketplaceCatalogService) CurrentPolicy(ctx context.Context) (MarketplacePolicyVersion,error) {
	c,err:=s.Current(ctx);if err!=nil{return MarketplacePolicyVersion{},err};return c.Policy,nil
}

func NewMarketplaceCatalogService(repo MarketplaceCatalogRepository) *MarketplaceCatalogService {
	return &MarketplaceCatalogService{repo: repo}
}

func cloneMarketplaceTiers(tiers []MarketplacePriceTier) []MarketplacePriceTier {
	copyPrice := func(price *Money) *Money {
		if price == nil { return nil }
		return &Money{Decimal: price.Decimal}
	}
	result := make([]MarketplacePriceTier, len(tiers))
	for i, t := range tiers {
		result[i] = MarketplacePriceTier{FromTokens: t.FromTokens, InputPerMillion: copyPrice(t.InputPerMillion),
			OutputPerMillion: copyPrice(t.OutputPerMillion), CacheReadPerMillion: copyPrice(t.CacheReadPerMillion), CacheWritePerMillion: copyPrice(t.CacheWritePerMillion)}
	}
	return result
}

func catalogTemplate(c *MarketplacePublishedCatalog, group MarketplaceCatalogGroup, model MarketplaceCatalogModel) QuoteSnapshot {
	var rate *decimal.Decimal
	if model.RateMultiplier != nil { d := *model.RateMultiplier; rate = &d }
	return QuoteSnapshot{Currency: "USD", PublicModel: model.PublicModel, ActualModel: model.ActualModel,
		Protocol: group.Protocol, PriceVersion: c.Version, PolicyVersion: c.Policy.Version, RateMultiplier: rate,
		Tiers: cloneMarketplaceTiers(model.Tiers), FeeBPS: c.Policy.FeeBPS, HoldHours: c.Policy.HoldHours,
		SnapshotAt: time.Now().UTC()}
}

func ValidateMarketplacePriceBook(book MarketplacePriceBook, policy MarketplacePolicyVersion) error {
	if len(book.Groups) == 0 || len(book.Groups) > 1000 || policy.FeeBPS < 0 || policy.FeeBPS > 10000 ||
		policy.HoldHours < 0 || policy.HoldHours > 87600 || policy.MaxSupplierConcurrency <= 0 ||
		policy.MinimumPayoutUSD.IsNegative() || !policy.MinimumPayoutUSD.Equal(policy.MinimumPayoutUSD.Round(2)) {
		return ErrMarketplacePriceUnavailable
	}
	groups := map[int64]bool{}
	for _, g := range book.Groups {
		if g.GroupID <= 0 || g.Name == "" || g.Platform == "" || groups[g.GroupID] || len(g.Models) == 0 || len(g.Models) > 1000 {
			return ErrMarketplacePriceUnavailable
		}
		groups[g.GroupID] = true
		models := map[string]bool{}
		for _, m := range g.Models {
			if m.PublicModel == "" || m.ActualModel == "" || models[m.PublicModel] || m.ContextLimit <= 0 ||
				m.ContextLimit > 4000000 || m.MaxOutputTokens <= 0 || m.MaxOutputTokens > m.ContextLimit {
				return ErrMarketplacePriceUnavailable
			}
			models[m.PublicModel] = true
			// Validation supplies temporary non-attribution identifiers only for the
			// pure quote validator. No attempt or supplier ledger is created here.
			q := catalogTemplate(&MarketplacePublishedCatalog{Version: 1, Policy: MarketplacePolicyVersion{Version: 1, FeeBPS: policy.FeeBPS, HoldHours: policy.HoldHours}}, g, m)
			q.AccountID, q.OfferID, q.OwnerUserID = 1, 1, 1
			if err := validateMarketplaceQuote(q); err != nil { return err }
		}
	}
	return nil
}

func (s *MarketplaceCatalogService) Current(ctx context.Context) (*MarketplacePublishedCatalog, error) {
	if s == nil || s.repo == nil { return nil, ErrMarketplaceCatalogUnavailable }
	c, err := s.repo.Current(ctx)
	if err != nil { return nil, err }
	if c == nil || c.Version <= 0 || c.Policy.Version <= 0 || c.PublishedAt.IsZero() { return nil, ErrMarketplaceCatalogUnavailable }
	if err := ValidateMarketplacePriceBook(c.Book, c.Policy); err != nil { return nil, err }
	return c, nil
}

func (s *MarketplaceCatalogService) IsSharedGroup(ctx context.Context, groupID int64) (bool, error) {
	if s == nil || s.repo == nil { return false, ErrMarketplaceCatalogUnavailable }
	return s.repo.IsSharedGroup(ctx, groupID)
}

// QuoteTemplate has no supplier attribution. The gateway must bind it to the
// actual selected account and snapshot consumer/account multipliers before dispatch.
func (s *MarketplaceCatalogService) QuoteTemplate(ctx context.Context, groupID int64, model, protocol string) (QuoteSnapshot, int64, int64, error) {
	c, err := s.Current(ctx)
	if err != nil { return QuoteSnapshot{}, 0, 0, err }
	for _, g := range c.Book.Groups {
		if g.GroupID != groupID || g.Protocol != protocol { continue }
		for _, m := range g.Models {
			if m.PublicModel == model { return catalogTemplate(c,g,m), m.ContextLimit, m.MaxOutputTokens, nil }
		}
	}
	return QuoteSnapshot{}, 0, 0, ErrMarketplacePriceUnavailable
}

func (s *MarketplaceCatalogService) Prices(ctx context.Context, platform string, selected []string) ([]SupplierModelPrice, error) {
	c, err := s.Current(ctx)
	if err != nil { return nil, err }
	wanted := map[string]bool{}
	for _, m := range selected { wanted[m] = false }
	prices := []SupplierModelPrice{}
	for _, g := range c.Book.Groups {
		if g.Platform != platform { continue }
		for _, m := range g.Models {
			if _, ok := wanted[m.ActualModel]; !ok { continue }
			wanted[m.ActualModel] = true
			prices = append(prices, SupplierModelPrice{GroupID:g.GroupID, Model:m.ActualModel,PublicModel:m.PublicModel,
				GroupName:g.Name,PriceVersion:c.Version,RateMultiplier:m.RateMultiplier.String(),Tiers:cloneMarketplaceTiers(m.Tiers)})
		}
	}
	for _, found := range wanted { if !found { return nil, ErrMarketplacePriceUnavailable } }
	return prices, nil
}

func (s *MarketplaceCatalogService) Publish(ctx context.Context, actor int64, policy MarketplacePolicyVersion, book MarketplacePriceBook) (*MarketplacePublishedCatalog, error) {
	if actor <= 0 { return nil, ErrMarketplaceCatalogUnavailable }
	if err := ValidateMarketplacePriceBook(book, policy); err != nil { return nil, err }
	return s.repo.Publish(ctx, actor, policy, book)
}

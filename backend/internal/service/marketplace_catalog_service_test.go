//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type catalogFixtureStore struct{ catalog *MarketplacePublishedCatalog }

func (s *catalogFixtureStore) Current(context.Context) (*MarketplacePublishedCatalog, error) { return s.catalog, nil }
func (s *catalogFixtureStore) IsSharedGroup(_ context.Context, id int64) (bool, error) { return id == 7, nil }
func (s *catalogFixtureStore) Publish(_ context.Context, _ int64, _ MarketplacePolicyVersion, _ MarketplacePriceBook) (*MarketplacePublishedCatalog, error) { return s.catalog, nil }

func publishedCatalogFixture() *MarketplacePublishedCatalog {
	q := validMarketplaceQuote()
	return &MarketplacePublishedCatalog{Version: 3, PublishedAt: time.Unix(100,0),
		Policy: MarketplacePolicyVersion{Version: 3, FeeBPS: 1000, HoldHours: 24, MaxSupplierConcurrency: 3, MinimumPayoutUSD: quoteMoney("1")},
		Book: MarketplacePriceBook{Groups: []MarketplaceCatalogGroup{{GroupID: 7, Name: "Shared", Platform: "openai", Protocol: "responses", Models: []MarketplaceCatalogModel{{
			PublicModel: "public-model", ActualModel: "actual-model", ContextLimit: 16000, MaxOutputTokens: 4000,
			RateMultiplier: quoteRate("1.5"), Tiers: q.Tiers,
		}}}}}}
}

func TestMarketplaceCatalogQuoteUsesPublishedModelAndPolicy(t *testing.T) {
	s := NewMarketplaceCatalogService(&catalogFixtureStore{catalog: publishedCatalogFixture()})
	q, contextLimit, maxOutput, err := s.QuoteTemplate(context.Background(), 7, "public-model", "responses")
	require.NoError(t, err)
	require.Equal(t, "actual-model", q.ActualModel)
	require.EqualValues(t, 3, q.PriceVersion)
	require.Equal(t, 1000, q.FeeBPS)
	require.EqualValues(t, 16000, contextLimit)
	require.EqualValues(t, 4000, maxOutput)
	require.Zero(t, q.AccountID, "templates must not pretend to attribute a supplier")
	_, _, _, err = s.QuoteTemplate(context.Background(), 7, "public-model", "chat_completions")
	require.ErrorIs(t, err, ErrMarketplacePriceUnavailable)
}

func TestMarketplaceCatalogSupplierPricesCoverEverySelectedModel(t *testing.T) {
	s := NewMarketplaceCatalogService(&catalogFixtureStore{catalog: publishedCatalogFixture()})
	prices, err := s.Prices(context.Background(), "openai", []string{"actual-model"})
	require.NoError(t, err)
	require.Len(t, prices, 1)
	require.EqualValues(t, 7, prices[0].GroupID)
	require.Equal(t, "1.5", prices[0].RateMultiplier)
	_, err = s.Prices(context.Background(), "openai", []string{"actual-model", "missing"})
	require.ErrorIs(t, err, ErrMarketplacePriceUnavailable)
}

func TestMarketplaceCatalogMissingPriceNeverPublishesAsFree(t *testing.T) {
	c := publishedCatalogFixture()
	c.Book.Groups[0].Models[0].Tiers[0].CacheReadPerMillion = nil
	require.Error(t, ValidateMarketplacePriceBook(c.Book, c.Policy))
	c = publishedCatalogFixture()
	c.Book.Groups[0].Models[0].MaxOutputTokens = 17000
	require.Error(t, ValidateMarketplacePriceBook(c.Book, c.Policy))
}

func TestMarketplaceCatalogQuoteCopiesMutablePointers(t *testing.T) {
	fixture := publishedCatalogFixture()
	s := NewMarketplaceCatalogService(&catalogFixtureStore{catalog: fixture})
	q, _, _, err := s.QuoteTemplate(context.Background(), 7, "public-model", "responses")
	require.NoError(t, err)
	q.Tiers[0].InputPerMillion.Decimal = decimal.NewFromInt(999)
	*q.RateMultiplier = decimal.NewFromInt(999)
	require.Equal(t, "2", fixture.Book.Groups[0].Models[0].Tiers[0].InputPerMillion.String())
	require.Equal(t, "1.5", fixture.Book.Groups[0].Models[0].RateMultiplier.String())
}

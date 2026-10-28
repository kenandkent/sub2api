//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func quoteMoney(s string) Money { return Money{Decimal: decimal.RequireFromString(s)} }
func quotePrice(s string) *Money { m := quoteMoney(s); return &m }
func quoteRate(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }

func validMarketplaceQuote() QuoteSnapshot {
	return QuoteSnapshot{Currency: "USD", PublicModel: "shared-text", ActualModel: "upstream-text",
		Protocol: "chat_completions", PriceVersion: 1, PolicyVersion: 1,
		RateMultiplier: quoteRate("1"), FeeBPS: 2000, HoldHours: 24,
		AccountID: 11, OfferID: 12, OwnerUserID: 13, SnapshotAt: time.Unix(100, 0).UTC(),
		Tiers: []MarketplacePriceTier{{FromTokens: 0, InputPerMillion: quotePrice("2"),
			OutputPerMillion: quotePrice("8"), CacheReadPerMillion: quotePrice("0.2"), CacheWritePerMillion: quotePrice("3")}}}
}

func TestMarketplaceQuoteCacheTokensAreNotDoubleCharged(t *testing.T) {
	q := validMarketplaceQuote()
	cost, err := NewMarketplaceQuoteService().CalculateActual(q, UsageSnapshot{
		InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 200, CacheWriteTokens: 100, Source: "upstream"})
	require.NoError(t, err)
	// 700 regular input + 200 cache read + 100 cache write + 100 output.
	require.Equal(t, "0.00254", cost.String())
}

func TestMarketplaceQuoteExplicitZeroAndMissingPrice(t *testing.T) {
	s := NewMarketplaceQuoteService()
	q := validMarketplaceQuote()
	q.Tiers = nil
	_, err := s.CalculateActual(q, UsageSnapshot{Source: "upstream"})
	require.ErrorIs(t, err, ErrMarketplacePriceUnavailable)
	q = validMarketplaceQuote()
	q.Tiers[0] = MarketplacePriceTier{InputPerMillion: quotePrice("0"), OutputPerMillion: quotePrice("0"), CacheReadPerMillion: quotePrice("0"), CacheWritePerMillion: quotePrice("0")}
	cost, err := s.CalculateActual(q, UsageSnapshot{InputTokens: 1000, Source: "upstream"})
	require.NoError(t, err)
	require.True(t, cost.IsZero())
}

func TestMarketplaceQuoteRejectsInvalidUsage(t *testing.T) {
	for _, usage := range []UsageSnapshot{
		{InputTokens: -1, Source: "upstream"},
		{InputTokens: 10, CacheReadTokens: 11, Source: "upstream"},
		{InputTokens: 10, CacheReadTokens: 6, CacheWriteTokens: 5, Source: "upstream"},
		{InputTokens: 10},
	} {
		_, err := NewMarketplaceQuoteService().CalculateActual(validMarketplaceQuote(), usage)
		require.ErrorIs(t, err, ErrMarketplaceUsageInvalid)
	}
}

func TestMarketplaceQuoteTierAndMultiplierSnapshot(t *testing.T) {
	q := validMarketplaceQuote()
	q.RateMultiplier = quoteRate("1.25")
	q.Tiers = append(q.Tiers, MarketplacePriceTier{FromTokens: 1000, InputPerMillion: quotePrice("4"),
		OutputPerMillion: quotePrice("16"), CacheReadPerMillion: quotePrice("0.4"), CacheWritePerMillion: quotePrice("6")})
	cost, err := NewMarketplaceQuoteService().CalculateActual(q, UsageSnapshot{InputTokens: 1000, OutputTokens: 10, Source: "upstream"})
	require.NoError(t, err)
	require.Equal(t, "0.0052", cost.String())
	digest, err := MarketplaceQuoteDigest(q)
	require.NoError(t, err)
	q.FeeBPS = 1000
	changed, err := MarketplaceQuoteDigest(q)
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
}

func TestMarketplaceQuoteUpperBoundCoversExpensiveCacheAndAllTiers(t *testing.T) {
	q := validMarketplaceQuote()
	q.Tiers = append(q.Tiers, MarketplacePriceTier{FromTokens: 1000, InputPerMillion: quotePrice("4"),
		OutputPerMillion: quotePrice("16"), CacheReadPerMillion: quotePrice("10"), CacheWritePerMillion: quotePrice("20")})
	budget, err := NewMarketplaceQuoteService().ComputeUpperBound(q, 1000, 100)
	require.NoError(t, err)
	require.Equal(t, "0.0216", budget.String())
	_, err = NewMarketplaceQuoteService().ComputeUpperBound(q, 1000, 0)
	require.ErrorIs(t, err, ErrMarketplaceBudgetInvalid)
}

func TestMarketplaceQuoteSplitConservesRoundedCharge(t *testing.T) {
	for _, bps := range []int{0, 1, 2000, 9999, 10000} {
		supplier, fee, err := SplitMarketplaceCharge(quoteMoney("0.01234567"), bps)
		require.NoError(t, err)
		require.True(t, supplier.Add(fee.Decimal).Equal(decimal.RequireFromString("0.01234567")))
		require.False(t, supplier.IsNegative())
		require.False(t, fee.IsNegative())
	}
	supplier, fee, err := SplitMarketplaceCharge(quoteMoney("0.01234567"), 2000)
	require.NoError(t, err)
	require.Equal(t, "0.00987654", supplier.String())
	require.Equal(t, "0.00246913", fee.String())
	_, _, err = SplitMarketplaceCharge(quoteMoney("1"), 10001)
	require.Error(t, err)
}

func TestMarketplaceQuoteMissingIndividualPriceIsNotFree(t *testing.T) {
	for _, field := range []string{"input_per_million", "output_per_million", "cache_read_per_million", "cache_write_per_million"} {
		q := validMarketplaceQuote()
		encoded, err := json.Marshal(q)
		require.NoError(t, err)
		var object map[string]any
		require.NoError(t, json.Unmarshal(encoded, &object))
		delete(object["tiers"].([]any)[0].(map[string]any), field)
		encoded, err = json.Marshal(object)
		require.NoError(t, err)
		var incomplete QuoteSnapshot
		require.NoError(t, json.Unmarshal(encoded, &incomplete))
		_, err = NewMarketplaceQuoteService().CalculateActual(incomplete, UsageSnapshot{InputTokens: 1, Source: "upstream"})
		require.ErrorIs(t, err, ErrMarketplacePriceUnavailable, field)
	}
}

func TestMarketplaceQuoteRoundsOnlyOnceAndRoundsBudgetUp(t *testing.T) {
	q := validMarketplaceQuote()
	q.Tiers[0].InputPerMillion = quotePrice("0.00499999996")
	cost, err := NewMarketplaceQuoteService().CalculateActual(q, UsageSnapshot{InputTokens: 1, Source: "upstream"})
	require.NoError(t, err)
	require.True(t, cost.IsZero(), cost.String())
	q.Tiers[0] = MarketplacePriceTier{InputPerMillion: quotePrice("0.00000000000000000001"), OutputPerMillion: quotePrice("0"), CacheReadPerMillion: quotePrice("0"), CacheWritePerMillion: quotePrice("0")}
	budget, err := NewMarketplaceQuoteService().ComputeUpperBound(q, 1, 1)
	require.NoError(t, err)
	require.Equal(t, "0.00000001", budget.String())
}

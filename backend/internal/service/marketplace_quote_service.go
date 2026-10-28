package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

var (
	ErrMarketplacePriceUnavailable = errors.New("marketplace price unavailable")
	ErrMarketplaceUsageInvalid     = errors.New("marketplace usage invalid")
	ErrMarketplaceBudgetInvalid    = errors.New("marketplace budget invalid")
)

// MarketplaceQuoteService calculates only from immutable request snapshots.
// InputTokens includes cache tokens; cache reads/writes replace ordinary input.
type MarketplaceQuoteService struct{}

func NewMarketplaceQuoteService() *MarketplaceQuoteService { return &MarketplaceQuoteService{} }

func validateMarketplaceQuote(q QuoteSnapshot) error {
	if q.Currency != "USD" || q.PublicModel == "" || q.ActualModel == "" ||
		(q.Protocol != "chat_completions" && q.Protocol != "responses") ||
		q.PriceVersion <= 0 || q.PolicyVersion <= 0 || q.AccountID <= 0 ||
		q.OfferID <= 0 || q.OwnerUserID <= 0 || q.SnapshotAt.IsZero() ||
		q.RateMultiplier == nil || q.RateMultiplier.IsNegative() || q.FeeBPS < 0 || q.FeeBPS > 10000 ||
		q.HoldHours < 0 || len(q.Tiers) == 0 {
		return ErrMarketplacePriceUnavailable
	}
	for i, tier := range q.Tiers {
		if tier.InputPerMillion == nil || tier.OutputPerMillion == nil || tier.CacheReadPerMillion == nil || tier.CacheWritePerMillion == nil ||
			(i == 0 && tier.FromTokens != 0) || (i > 0 && tier.FromTokens <= q.Tiers[i-1].FromTokens) ||
			tier.InputPerMillion.IsNegative() || tier.OutputPerMillion.IsNegative() ||
			tier.CacheReadPerMillion.IsNegative() || tier.CacheWritePerMillion.IsNegative() {
			return ErrMarketplacePriceUnavailable
		}
	}
	return nil
}

func (s *MarketplaceQuoteService) CalculateActual(q QuoteSnapshot, usage UsageSnapshot) (Money, error) {
	if err := validateMarketplaceQuote(q); err != nil {
		return Money{}, err
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 ||
		usage.CacheReadTokens > usage.InputTokens || usage.CacheWriteTokens > usage.InputTokens-usage.CacheReadTokens ||
		usage.Source != "upstream" {
		return Money{}, ErrMarketplaceUsageInvalid
	}
	tier := q.Tiers[0]
	for _, next := range q.Tiers[1:] {
		if usage.InputTokens < next.FromTokens {
			break
		}
		tier = next
	}
	regularInput := usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
	cost := tier.InputPerMillion.Mul(decimal.NewFromInt(regularInput)).
		Add(tier.CacheReadPerMillion.Mul(decimal.NewFromInt(usage.CacheReadTokens))).
		Add(tier.CacheWritePerMillion.Mul(decimal.NewFromInt(usage.CacheWriteTokens))).
		Add(tier.OutputPerMillion.Mul(decimal.NewFromInt(usage.OutputTokens))).
		Mul(*q.RateMultiplier).Shift(-6).Round(8)
	if cost.GreaterThanOrEqual(decimal.NewFromInt(1000000000000)) {
		return Money{}, fmt.Errorf("%w: amount exceeds numeric(20,8)", ErrMarketplaceUsageInvalid)
	}
	return Money{Decimal: cost}, nil
}

// ComputeUpperBound uses the most expensive input category and tier. The caller
// must enforce both token caps in the upstream adapter; estimates are not caps.
func (s *MarketplaceQuoteService) ComputeUpperBound(q QuoteSnapshot, maxInputTokens, maxOutputTokens int64) (Money, error) {
	if err := validateMarketplaceQuote(q); err != nil {
		return Money{}, err
	}
	if maxInputTokens <= 0 || maxOutputTokens <= 0 {
		return Money{}, ErrMarketplaceBudgetInvalid
	}
	inputMax, outputMax := decimal.Zero, decimal.Zero
	for _, tier := range q.Tiers {
		inputMax = decimal.Max(inputMax, tier.InputPerMillion.Decimal, tier.CacheReadPerMillion.Decimal, tier.CacheWritePerMillion.Decimal)
		outputMax = decimal.Max(outputMax, tier.OutputPerMillion.Decimal)
	}
	budget := inputMax.Mul(decimal.NewFromInt(maxInputTokens)).
		Add(outputMax.Mul(decimal.NewFromInt(maxOutputTokens))).Mul(*q.RateMultiplier).
		Shift(-6).RoundCeil(8)
	if budget.GreaterThanOrEqual(decimal.NewFromInt(1000000000000)) {
		return Money{}, ErrMarketplaceBudgetInvalid
	}
	return Money{Decimal: budget}, nil
}

func SplitMarketplaceCharge(charge Money, feeBPS int) (supplier Money, fee Money, err error) {
	if charge.IsNegative() || !charge.Equal(charge.Round(8)) || feeBPS < 0 || feeBPS > 10000 {
		return Money{}, Money{}, ErrMarketplaceUsageInvalid
	}
	net := charge.Mul(decimal.NewFromInt(int64(10000 - feeBPS))).Shift(-4).Round(8)
	return Money{Decimal: net}, Money{Decimal: charge.Sub(net)}, nil
}

func MarketplaceQuoteDigest(q QuoteSnapshot) (string, error) {
	if err := validateMarketplaceQuote(q); err != nil {
		return "", err
	}
	data, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

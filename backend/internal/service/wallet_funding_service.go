package service

import (
	"errors"
	"sort"

	"github.com/shopspring/decimal"
)

// Funding values are denominated in USD and rounded once to eight decimal places.
type FundingMoney = decimal.Decimal
type FundingKind string

const (
	FundingCashPaid    FundingKind = "cash_paid"
	FundingPromotional FundingKind = "promotional"
	FundingLegacy      FundingKind = "legacy_unverified"
	FundingSubsidy     FundingKind = "platform_subsidy"
)

var ErrInsufficientFunding = errors.New("insufficient eligible funding")
var ErrInvalidFunding = errors.New("invalid funding amount or lot")

type FundingLot struct {
	ID                  int64
	Kind                FundingKind
	Remaining, Reserved FundingMoney
}
type FundingPortion struct {
	LotID  int64
	Amount FundingMoney
}

func QuantizeFunding(v FundingMoney) FundingMoney { return v.Round(8) }

// FundingApplyDebt divides newly available money between debt repayment and a lot.
func FundingApplyDebt(amount, debt FundingMoney) (paid, available FundingMoney) {
	amount, debt = QuantizeFunding(amount), QuantizeFunding(debt)
	if amount.IsNegative() || debt.IsNegative() {
		return decimal.Zero, decimal.Zero
	}
	paid = decimal.Min(amount, debt)
	return paid, amount.Sub(paid)
}

// PlanFundingConsumption chooses only unreserved funds. Shared consumption uses
// eligible lots in FIFO order; ordinary consumption uses the documented kind priority.
func PlanFundingConsumption(lots []FundingLot, amount FundingMoney, shared bool) ([]FundingPortion, error) {
	amount = QuantizeFunding(amount)
	if amount.IsNegative() {
		return nil, ErrInvalidFunding
	}
	ordered := append([]FundingLot(nil), lots...)
	rank := func(k FundingKind) int {
		switch k {
		case FundingPromotional:
			return 0
		case FundingLegacy:
			return 1
		case FundingSubsidy:
			return 2
		case FundingCashPaid:
			return 3
		}
		return 4
	}
	for _, lot := range ordered {
		if rank(lot.Kind) == 4 || lot.Reserved.IsNegative() || lot.Remaining.LessThan(lot.Reserved) {
			return nil, ErrInvalidFunding
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !shared && rank(ordered[i].Kind) != rank(ordered[j].Kind) {
			return rank(ordered[i].Kind) < rank(ordered[j].Kind)
		}
		return ordered[i].ID < ordered[j].ID
	})
	result := make([]FundingPortion, 0)
	for _, lot := range ordered {
		if amount.IsZero() {
			break
		}
		if shared && lot.Kind != FundingCashPaid && lot.Kind != FundingSubsidy {
			continue
		}
		take := decimal.Min(amount, lot.Remaining.Sub(lot.Reserved))
		if take.IsPositive() {
			result = append(result, FundingPortion{LotID: lot.ID, Amount: take})
			amount = amount.Sub(take)
		}
	}
	if amount.IsPositive() {
		return nil, ErrInsufficientFunding
	}
	return result, nil
}

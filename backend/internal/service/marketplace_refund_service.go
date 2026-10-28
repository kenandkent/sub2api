package service

import (
	"context"
	"errors"
	"github.com/shopspring/decimal"
	"strings"
)

var ErrMarketplaceRefundInvalid = errors.New("invalid marketplace refund")

// CumulativeRefund computes cumulative supplier and platform reversal from the
// original capture, so partial refunds never accumulate rounding errors.
func CumulativeRefund(charge, net, refunded decimal.Decimal) (decimal.Decimal, decimal.Decimal, error) {
	if !charge.IsPositive() || net.IsNegative() || net.GreaterThan(charge) || refunded.IsNegative() || refunded.GreaterThan(charge) || !charge.Equal(charge.Round(8)) || !net.Equal(net.Round(8)) || !refunded.Equal(refunded.Round(8)) {
		return decimal.Zero, decimal.Zero, ErrMarketplaceRefundInvalid
	}
	supplier := net
	if refunded.LessThan(charge) {
		supplier = net.Mul(refunded).DivRound(charge, 8)
	}
	return supplier, refunded.Sub(supplier), nil
}

// MarketplaceRefundStore carries the full refund transaction, including actor authorization.
type MarketplaceRefundStore interface {
	Refund(context.Context, string, int64, decimal.Decimal, string) error
}
type MarketplaceRefundService struct{ store MarketplaceRefundStore }

func NewMarketplaceRefundService(store MarketplaceRefundStore) *MarketplaceRefundService {
	return &MarketplaceRefundService{store: store}
}
func (s *MarketplaceRefundService) Refund(ctx context.Context, receipt string, actor int64, amount decimal.Decimal, operation string) error {
	if s == nil || s.store == nil || strings.TrimSpace(receipt) == "" || actor <= 0 || !amount.IsPositive() || !amount.Equal(amount.Round(8)) || strings.TrimSpace(operation) == "" || len(operation) > 255 {
		return ErrMarketplaceRefundInvalid
	}
	return s.store.Refund(ctx, receipt, actor, amount, operation)
}

// marketplaceRefundReasonStore is an optional extension to preserve compatibility
// with existing MarketplaceRefundStore implementations. Administrative refunds
// fail closed when the store cannot persist the reason.
type marketplaceRefundReasonStore interface {
	RefundWithReason(context.Context, string, int64, decimal.Decimal, string, string) error
}

func (s *MarketplaceRefundService) RefundWithReason(ctx context.Context, receipt string, actor int64, amount decimal.Decimal, operation, reason string) error {
	reason = strings.TrimSpace(reason)
	operation = strings.TrimSpace(operation)
	if s == nil || s.store == nil || strings.TrimSpace(receipt) == "" || actor <= 0 || !amount.IsPositive() || !amount.Equal(amount.Round(8)) || operation == "" || len(operation) > 255 || reason == "" || len(reason) > 4096 {
		return ErrMarketplaceRefundInvalid
	}
	store, ok := s.store.(marketplaceRefundReasonStore)
	if !ok {
		return ErrMarketplaceRefundInvalid
	}
	return store.RefundWithReason(ctx, receipt, actor, amount, operation, reason)
}

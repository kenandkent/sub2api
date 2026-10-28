package service

import (
	"context"
	"github.com/shopspring/decimal"
	"testing"
)

type reasonRefundStore struct {
	reason string
	MarketplaceRefundStore
}

func (s *reasonRefundStore) RefundWithReason(_ context.Context, _ string, _ int64, _ decimal.Decimal, _ string, reason string) error {
	s.reason = reason
	return nil
}
func TestRefundWithReasonRequiresAndPassesReason(t *testing.T) {
	s := &reasonRefundStore{}
	svc := NewMarketplaceRefundService(s)
	if err := svc.RefundWithReason(context.Background(), "receipt", 1, decimal.NewFromInt(1), "op", " "); err == nil {
		t.Fatal("empty reason accepted")
	}
	if err := svc.RefundWithReason(context.Background(), "receipt", 1, decimal.NewFromInt(1), "op", " risk "); err != nil || s.reason != "risk" {
		t.Fatalf("reason not passed: %q %v", s.reason, err)
	}
}

package service

import (
	"context"
	"database/sql"
	"errors"
	"github.com/shopspring/decimal"
	"time"
)

// MaturityAmount is the unrefunded portion that was actually credited to pending.
func MaturityAmount(net, debtOffset, refundedNet decimal.Decimal) decimal.Decimal {
	return decimal.Max(net.Sub(debtOffset).Sub(refundedNet), decimal.Zero)
}

type SupplierSettlementStore interface {
	MatureDue(context.Context, time.Time, int) (int, error)
}
type SupplierSettlementService struct{ store SupplierSettlementStore }

func NewSupplierSettlementService(store SupplierSettlementStore) *SupplierSettlementService {
	return &SupplierSettlementService{store: store}
}
func (s *SupplierSettlementService) MatureDue(ctx context.Context, now time.Time, limit int) (int, error) {
	return s.store.MatureDue(ctx, now.UTC(), limit)
}
func (s *SupplierSettlementService) MatureDueOnConn(ctx context.Context, conn *sql.Conn, now time.Time, limit int) (int, error) {
	store, ok := s.store.(interface {
		MatureDueOnConn(context.Context, *sql.Conn, time.Time, int) (int, error)
	})
	if !ok || conn == nil {
		return 0, errors.New("settlement connection unavailable")
	}
	return store.MatureDueOnConn(ctx, conn, now.UTC(), limit)
}

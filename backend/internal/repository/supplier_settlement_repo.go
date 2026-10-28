package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type SupplierSettlementRepository struct{ db *sql.DB }

func NewSupplierSettlementRepository(db *sql.DB) *SupplierSettlementRepository {
	return &SupplierSettlementRepository{db: db}
}

// MatureCapture serializes with refunds and payouts on the supplier wallet before
// locking the capture. A repeated or concurrent call cannot release twice.
func (r *SupplierSettlementRepository) MatureCapture(ctx context.Context, id int64, now time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM marketplace_captures WHERE id=$1`, id).Scan(&owner); err != nil {
		return false, err
	}
	var pending decimal.Decimal
	if err = tx.QueryRowContext(ctx, `SELECT pending FROM supplier_wallets WHERE user_id=$1 AND currency='USD' FOR UPDATE`, owner).Scan(&pending); err != nil {
		return false, err
	}
	var frozen bool
	if err = tx.QueryRowContext(ctx, `SELECT payout_frozen FROM supplier_profiles WHERE user_id=$1 FOR UPDATE`, owner).Scan(&frozen); err != nil {
		return false, err
	}
	var receipt, status string
	var matures time.Time
	var net, offset, refunded decimal.Decimal
	if err = tx.QueryRowContext(ctx, `SELECT receipt_id,status,matures_at,supplier_net,supplier_debt_offset,refunded_net FROM marketplace_captures WHERE id=$1 FOR UPDATE`, id).Scan(&receipt, &status, &matures, &net, &offset, &refunded); err != nil {
		return false, err
	}
	if frozen || status != "pending" || matures.After(now) {
		return false, nil
	}
	amount := service.MaturityAmount(net, offset, refunded)
	if pending.LessThan(amount) {
		return false, fmt.Errorf("maturity pending invariant: capture %d", id)
	}
	if amount.IsPositive() {
		if _, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET pending=pending-$2,available=available+$2,version=version+1 WHERE user_id=$1 AND currency='USD'`, owner, amount); err != nil {
			return false, err
		}
		entries := []LedgerPosting{{Account: "supplier_pending", SubjectID: &owner, Side: "debit", Amount: amount}, {Account: "supplier_available", SubjectID: &owner, Side: "credit", Amount: amount}}
		if err = NewMarketplaceLedgerRepository().Post(ctx, tx, "marketplace:maturity:"+receipt, marketplaceActor, "USD", entries); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_captures SET status='matured' WHERE id=$1`, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// MatureDue reads candidates without holding row locks, then rechecks each after
// acquiring its wallet lock; multiple instances may safely scan the same rows.
func (r *SupplierSettlementRepository) MatureDue(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("invalid maturity limit")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT c.id FROM marketplace_captures c JOIN supplier_profiles p ON p.user_id=c.owner_user_id WHERE c.status='pending' AND c.matures_at<=$1 AND NOT p.payout_frozen ORDER BY c.matures_at,c.id LIMIT $2`, now, limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		ok, e := r.MatureCapture(ctx, id, now)
		if e != nil {
			return count, e
		}
		if ok {
			count++
		}
	}
	return count, nil
}

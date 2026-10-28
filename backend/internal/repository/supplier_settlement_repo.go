package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type SupplierSettlementRepository struct{ db *sql.DB }

type settlementConnection interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func NewSupplierSettlementRepository(db *sql.DB) *SupplierSettlementRepository {
	return &SupplierSettlementRepository{db: db}
}

// MatureCapture serializes with refunds and payouts on the supplier wallet before
// locking the capture. A repeated or concurrent call cannot release twice.
func (r *SupplierSettlementRepository) MatureCapture(ctx context.Context, id int64, now time.Time) (bool, error) {
	return r.matureCapture(ctx, r.db, id, now)
}
func (r *SupplierSettlementRepository) matureCapture(ctx context.Context, db settlementConnection, id int64, now time.Time) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// A single locked wallet or period must not hold the shared coordinator
	// for its whole pass. PostgreSQL lock timeout leaves the connection usable
	// so this capture can be recorded for retry without cancelling the session.
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '1s'`); err != nil {
		return false, err
	}
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
	return r.matureDue(ctx, r.db, now, limit)
}

// MatureDueOnConn keeps the coordinator lock and all settlement transactions
// on the same reserved connection, including with a one-connection pool.
func (r *SupplierSettlementRepository) MatureDueOnConn(ctx context.Context, conn *sql.Conn, now time.Time, limit int) (int, error) {
	return r.matureDue(ctx, conn, now, limit)
}
func (r *SupplierSettlementRepository) matureDue(ctx context.Context, db settlementConnection, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("invalid maturity limit")
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id FROM marketplace_captures c JOIN supplier_profiles p ON p.user_id=c.owner_user_id LEFT JOIN marketplace_maturity_failures f ON f.capture_id=c.id WHERE c.status='pending' AND c.matures_at<=$1 AND NOT p.payout_frozen AND (f.capture_id IS NULL OR f.retry_after<=now()) ORDER BY c.matures_at,c.id LIMIT $2`, now, limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	var failures []error
	for _, id := range ids {
		ok, e := r.matureCapture(ctx, db, id, now)
		if e != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			// Quarantine a failed item briefly so a permanently inconsistent
			// wallet cannot keep all later suppliers from maturing.
			_, recordErr := db.ExecContext(ctx, `INSERT INTO marketplace_maturity_failures(capture_id,attempts,last_failed_at,retry_after) VALUES($1,1,now(),now()+interval '1 minute') ON CONFLICT(capture_id) DO UPDATE SET attempts=marketplace_maturity_failures.attempts+1,last_failed_at=now(),retry_after=now()+interval '1 minute'`, id)
			if recordErr != nil {
				return count, fmt.Errorf("record maturity failure for capture %d: %w", id, errors.Join(e, recordErr))
			}
			slog.Error("marketplace capture maturity deferred", "capture_id", id, "error", e)
			failures = append(failures, fmt.Errorf("capture %d: %w", id, e))
			continue
		}
		if _, e = db.ExecContext(ctx, `DELETE FROM marketplace_maturity_failures WHERE capture_id=$1`, id); e != nil {
			return count, e
		}
		if ok {
			count++
		}
	}
	return count, errors.Join(failures...)
}

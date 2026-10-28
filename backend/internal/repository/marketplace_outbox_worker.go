package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// MarketplaceOutboxEvent joins reference-only outbox rows to capture metadata;
// it never contains prompts or credentials.
type MarketplaceOutboxEvent struct {
	ID                     int64
	EventID                string
	EventType              string
	ReceiptID              string
	CaptureID              int64
	BuyerUserID            int64
	APIKeyID               int64
	AccountID              int64
	APIKeyQuotaExhausted   bool
	APIKeyRateLimitChanged bool
	// Capture-time balance is historical and must not overwrite newer cache state.
	BuyerBalanceAfter decimal.Decimal
}

// MarketplaceOutboxProjector writes a projection in the supplied transaction.
// Its writes and the processed marker commit together, so a retry is safe.
type MarketplaceOutboxProjector func(context.Context, *sql.Tx, MarketplaceOutboxEvent) error

type MarketplaceOutboxWorker struct {
	DB      *sql.DB
	Project MarketplaceOutboxProjector
}

func (w *MarketplaceOutboxWorker) ProcessOne(ctx context.Context) (bool, error) {
	if w.DB == nil || w.Project == nil {
		return false, errors.New("marketplace outbox worker not configured")
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var e MarketplaceOutboxEvent
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.event_id,o.event_type,o.receipt_id::text,o.capture_id,r.buyer_user_id,r.api_key_id,a.account_id,c.api_key_quota_exhausted,c.api_key_rate_limit_changed,c.buyer_balance_after FROM marketplace_outbox o JOIN marketplace_captures c ON c.id=o.capture_id JOIN marketplace_requests r ON r.id=o.receipt_id JOIN marketplace_attempts a ON a.id=c.attempt_id WHERE o.processed_at IS NULL AND o.next_attempt_at<=now() ORDER BY o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1`).Scan(&e.ID, &e.EventID, &e.EventType, &e.ReceiptID, &e.CaptureID, &e.BuyerUserID, &e.APIKeyID, &e.AccountID, &e.APIKeyQuotaExhausted, &e.APIKeyRateLimitChanged, &e.BuyerBalanceAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT marketplace_projection`); err != nil {
		return false, err
	}
	projectErr := w.Project(ctx, tx, e)
	if projectErr != nil {
		if _, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT marketplace_projection`); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE marketplace_outbox SET attempts=attempts+1,next_attempt_at=now()+interval '1 minute' WHERE id=$1`, e.ID); err != nil {
			return false, err
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE marketplace_outbox SET processed_at=now(),attempts=attempts+1 WHERE id=$1`, e.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, projectErr
}

// Run exits on cancellation; failed projections stay durable and become ready again.
func (w *MarketplaceOutboxWorker) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_, _ = w.ProcessOne(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

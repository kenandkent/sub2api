package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

type MarketplaceBalanceInvalidationWorker struct {
	DB    *sql.DB
	Cache MarketplaceCacheInvalidator
}

func NewMarketplaceBalanceInvalidationWorker(db *sql.DB, cache MarketplaceCacheInvalidator) *MarketplaceBalanceInvalidationWorker {
	return &MarketplaceBalanceInvalidationWorker{DB: db, Cache: cache}
}

// ProcessOne uses a lease and a second delayed deletion to cover racing cache
// fills. Failed deliveries remain durable and retryable.
func (w *MarketplaceBalanceInvalidationWorker) ProcessOne(ctx context.Context) (bool, error) {
	if w == nil || w.DB == nil || w.Cache == nil {
		return false, errors.New("marketplace balance invalidator unavailable")
	}
	token := uuid.NewString()
	var id, user int64
	var stage int
	err := w.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM marketplace_balance_invalidation_tasks WHERE processed_at IS NULL AND available_at<=now() AND (claimed_at IS NULL OR claimed_at<now()-interval '1 minute') ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE marketplace_balance_invalidation_tasks t SET claimed_at=now(),claim_token=$1 FROM candidate WHERE t.id=candidate.id RETURNING t.id,t.user_id,t.delivery_stage`, token).Scan(&id, &user, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = w.Cache.InvalidateUserBalance(ctx, user); err != nil {
		_, updateErr := w.DB.ExecContext(ctx, `UPDATE marketplace_balance_invalidation_tasks SET attempts=attempts+1,available_at=now()+interval '1 minute',claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`, id, token)
		return true, errors.Join(err, updateErr)
	}
	query := `UPDATE marketplace_balance_invalidation_tasks SET attempts=attempts+1,processed_at=now(),claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`
	if stage == 0 {
		query = `UPDATE marketplace_balance_invalidation_tasks SET attempts=attempts+1,delivery_stage=1,available_at=now()+interval '30 seconds',claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`
	}
	res, err := w.DB.ExecContext(ctx, query, id, token)
	if err != nil {
		return true, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return true, err
	}
	if n != 1 {
		return true, errors.New("marketplace balance invalidation lease expired")
	}
	return true, nil
}

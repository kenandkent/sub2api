package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type MarketplaceCacheInvalidator interface {
	InvalidateUserBalance(context.Context, int64) error
	InvalidateAPIKeyRateLimit(context.Context, int64) error
}
type MarketplaceCacheWorker struct {
	DB    *sql.DB
	Cache MarketplaceCacheInvalidator
}

func NewMarketplaceCacheWorker(db *sql.DB, cache MarketplaceCacheInvalidator) *MarketplaceCacheWorker {
	return &MarketplaceCacheWorker{DB: db, Cache: cache}
}

// ProcessOne commits its claim before calling the cache. A lease permits retries
// after crashes; both invalidations are idempotent and failure remains durable.
func (w *MarketplaceCacheWorker) ProcessOne(ctx context.Context) (bool, error) {
	if w.DB == nil || w.Cache == nil {
		return false, errors.New("marketplace cache worker not configured")
	}
	token := uuid.NewString()
	var id, user, key int64
	var stage int
	err := w.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM marketplace_cache_tasks WHERE processed_at IS NULL AND available_at<=now() AND (claimed_at IS NULL OR claimed_at<now()-interval '1 minute') ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE marketplace_cache_tasks t SET claimed_at=now(),claim_token=$1 FROM candidate WHERE t.id=candidate.id RETURNING t.id,t.user_id,t.api_key_id,t.delivery_stage`, token).Scan(&id, &user, &key, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = w.Cache.InvalidateUserBalance(ctx, user)
	if err == nil {
		err = w.Cache.InvalidateAPIKeyRateLimit(ctx, key)
	}
	if err != nil {
		_, updateErr := w.DB.ExecContext(ctx, `UPDATE marketplace_cache_tasks SET attempts=attempts+1,available_at=now()+interval '1 minute',claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`, id, token)
		if updateErr != nil {
			return true, errors.Join(err, updateErr)
		}
		return true, err
	}
	// A delayed second pass clears entries populated by a read that raced the first deletion.
	query := `UPDATE marketplace_cache_tasks SET attempts=attempts+1,processed_at=now(),claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`
	if stage == 0 {
		query = `UPDATE marketplace_cache_tasks SET attempts=attempts+1,delivery_stage=1,available_at=now()+interval '30 seconds',claimed_at=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2`
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
		return true, errors.New("marketplace cache claim expired")
	}
	return true, nil
}

// Stale requests are reviewed after two hours of inactivity, well beyond the
// default upstream header timeout. Review does not prove a request has ended
// and never releases its hold.
const marketplaceStaleReviewReason = "request inactive for over two hours; manual review required"

// MarketplaceBackgroundWorker continues after marketplace dispatch is disabled.
type MarketplaceBackgroundWorker struct {
	db                  *sql.DB
	outbox              *MarketplaceOutboxWorker
	cache               *MarketplaceCacheWorker
	balanceInvalidation *MarketplaceBalanceInvalidationWorker
	settlement          *service.SupplierSettlementService
	finance             *service.SupplierFinanceService
	ctx                 context.Context
	cancel              context.CancelFunc
	start, stop         sync.Once
	wg                  sync.WaitGroup
}

func NewMarketplaceBackgroundWorker(db *sql.DB, cache MarketplaceCacheInvalidator, settlement *service.SupplierSettlementService, finance *service.SupplierFinanceService) *MarketplaceBackgroundWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketplaceBackgroundWorker{db: db, outbox: NewMarketplaceOutboxWorker(db), cache: NewMarketplaceCacheWorker(db, cache), balanceInvalidation: NewMarketplaceBalanceInvalidationWorker(db, cache), settlement: settlement, finance: finance, ctx: ctx, cancel: cancel}
}
func (w *MarketplaceBackgroundWorker) Start() {
	if w == nil || w.db == nil || w.cache.Cache == nil || w.settlement == nil || w.finance == nil {
		return
	}
	w.start.Do(func() {
		for _, run := range []func(){w.runProjection, w.runBalanceInvalidation, w.runRecovery, w.runMaturity, w.runStatements} {
			w.wg.Add(1)
			go func(f func()) { defer w.wg.Done(); f() }(run)
		}
	})
}
func (w *MarketplaceBackgroundWorker) Stop() {
	if w == nil {
		return
	}
	w.stop.Do(func() { w.cancel(); w.wg.Wait() })
}
func (w *MarketplaceBackgroundWorker) every(interval time.Duration, fn func()) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if w.ctx.Err() != nil {
			return
		}
		fn()
		select {
		case <-w.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Keep projection and cache work independent of potentially slow finance scans.
func (w *MarketplaceBackgroundWorker) runProjection() {
	w.every(time.Second, func() {
		for i := 0; i < 100; i++ {
			done, err := w.outbox.ProcessOne(w.ctx)
			if err != nil {
				if w.ctx.Err() == nil {
					slog.Error("marketplace projection failed", "error", err)
				}
				break
			}
			if !done {
				break
			}
		}
		for i := 0; i < 100; i++ {
			done, err := w.cache.ProcessOne(w.ctx)
			if err != nil {
				if w.ctx.Err() == nil {
					slog.Error("marketplace cache invalidation failed", "error", err)
				}
				break
			}
			if !done {
				break
			}
		}
	})
}
func (w *MarketplaceBackgroundWorker) runRecovery() {
	w.every(time.Minute, func() {
		count, err := w.reviewStaleReceipts(w.ctx)
		if err != nil && w.ctx.Err() == nil {
			slog.Error("marketplace stale receipt scan failed", "error", err)
		} else if count > 0 {
			slog.Warn("marketplace stale receipts require review", "count", count)
		}
	})
}
func (w *MarketplaceBackgroundWorker) runBalanceInvalidation() {
	w.every(time.Second, func() {
		for i := 0; i < 100; i++ {
			done, err := w.balanceInvalidation.ProcessOne(w.ctx)
			if err != nil {
				if w.ctx.Err() == nil {
					slog.Error("marketplace balance invalidation failed", "error", err)
				}
				break
			}
			if !done {
				break
			}
		}
	})
}

func drainMaturityBatches(ctx context.Context, batchSize, maxBatches int, process func(context.Context, int) (int, error)) (int, error) {
	total := 0
	for i := 0; i < maxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := process(ctx, batchSize)
		total += n
		if err != nil {
			return total, err
		}
		if n < batchSize {
			break
		}
	}
	return total, nil
}

// A session lock prevents instances selecting the same earliest batch. All
// settlement transactions reuse this connection, even with a pool size of one.
func (w *MarketplaceBackgroundWorker) maturePass(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := w.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	const lockID int64 = 75084321640951481
	var locked bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); err != nil {
		// An interrupted query may have acquired the lock before its reply was
		// lost; discard the session rather than returning it to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&released); err != nil || !released {
			// Never return a connection that may still hold a session lock to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			slog.Error("marketplace maturity unlock failed", "error", err)
		}
	}()
	_, err = drainMaturityBatches(ctx, 100, 10, func(ctx context.Context, limit int) (int, error) {
		return w.settlement.MatureDueOnConn(ctx, conn, time.Now().UTC(), limit)
	})
	return err
}
func (w *MarketplaceBackgroundWorker) runMaturity() {
	w.every(time.Second, func() {
		if err := w.maturePass(w.ctx); err != nil && w.ctx.Err() == nil {
			slog.Error("marketplace maturity failed", "error", err)
		}
	})
}
func (w *MarketplaceBackgroundWorker) runStatements() {
	w.every(time.Minute, func() {
		if err := w.closeMonths(w.ctx); err != nil && w.ctx.Err() == nil {
			slog.Error("marketplace statement generation failed", "error", err)
		}
	})
}

// reviewStaleReceipts changes only receipt state and reason. The lock skips
// active transactions and serializes with dispatch, settlement and recovery.
func (w *MarketplaceBackgroundWorker) reviewStaleReceipts(ctx context.Context) (int64, error) {
	result, err := w.db.ExecContext(ctx, `WITH candidates AS (
		SELECT id FROM marketplace_requests
		WHERE status IN ('prepared','budget_reserved','dispatched','usage_pending')
		  AND updated_at < now() - interval '2 hours'
		ORDER BY updated_at, id FOR UPDATE SKIP LOCKED LIMIT 100
	) UPDATE marketplace_requests r
	  SET status='needs_review',review_reason=$1,updated_at=now()
	  FROM candidates WHERE r.id=candidates.id`, marketplaceStaleReviewReason)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (w *MarketplaceBackgroundWorker) closeMonths(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `SELECT p.user_id, months.month FROM supplier_profiles p CROSS JOIN LATERAL generate_series(date_trunc('month',p.created_at AT TIME ZONE 'UTC'),date_trunc('month',now() AT TIME ZONE 'UTC')-interval '1 month',interval '1 month') AS months(month) WHERE NOT EXISTS(SELECT 1 FROM supplier_statements s WHERE s.supplier_id=p.user_id AND s.month=months.month::date) ORDER BY months.month,p.user_id LIMIT 100`)
	if err != nil {
		return err
	}
	type period struct {
		id    int64
		month time.Time
	}
	var periods []period
	for rows.Next() {
		var p period
		if err = rows.Scan(&p.id, &p.month); err != nil {
			_ = rows.Close()
			return err
		}
		periods = append(periods, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range periods {
		m := time.Date(p.month.Year(), p.month.Month(), 1, 0, 0, 0, 0, time.UTC)
		if _, err = w.finance.GenerateClosedMonth(ctx, p.id, m); err != nil {
			return fmt.Errorf("supplier %d month %s: %w", p.id, m.Format("2006-01"), err)
		}
	}
	return nil
}

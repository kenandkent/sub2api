package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type marketplaceCatalogRepository struct{ db *sql.DB }

func NewMarketplaceCatalogRepository(db *sql.DB) service.MarketplaceCatalogRepository {
	return &marketplaceCatalogRepository{db: db}
}

func (r *marketplaceCatalogRepository) IsSharedGroup(ctx context.Context, groupID int64) (bool, error) {
	var shared bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_group_registry WHERE group_id=$1)`, groupID).Scan(&shared)
	return shared, err
}

func (r *marketplaceCatalogRepository) Current(ctx context.Context) (*service.MarketplacePublishedCatalog, error) {
	c := &service.MarketplacePublishedCatalog{}
	var body []byte
	err := r.db.QueryRowContext(ctx, `
 SELECT p.version,p.published_at,p.rules,s.version,s.fee_bps,s.hold_hours,
        s.max_supplier_concurrency,s.minimum_payout_usd,s.published_by,s.published_at
 FROM marketplace_price_versions p JOIN marketplace_policies s ON s.version=p.version
 WHERE p.published_at IS NOT NULL AND s.published_at IS NOT NULL
 ORDER BY p.version DESC LIMIT 1`).Scan(&c.Version, &c.PublishedAt, &body, &c.Policy.Version, &c.Policy.FeeBPS,
		&c.Policy.HoldHours, &c.Policy.MaxSupplierConcurrency, &c.Policy.MinimumPayoutUSD, &c.Policy.PublishedBy, &c.Policy.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrMarketplaceCatalogUnavailable
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &c.Book); err != nil {
		return nil, fmt.Errorf("invalid published marketplace catalog: %w", err)
	}
	if err = service.ValidateMarketplacePriceBook(c.Book, c.Policy); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *marketplaceCatalogRepository) Publish(ctx context.Context, actor int64, policy service.MarketplacePolicyVersion, book service.MarketplacePriceBook) (*service.MarketplacePublishedCatalog, error) {
	if err := service.ValidateMarketplacePriceBook(book, policy); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(738241016260)`); err != nil {
		return nil, err
	}
	var admin bool
	if err = tx.QueryRowContext(ctx, `SELECT role='admin' AND status='active' AND deleted_at IS NULL FROM users WHERE id=$1`, actor).Scan(&admin); err != nil {
		return nil, err
	}
	if !admin {
		return nil, errors.New("active administrator required")
	}
	for i, g := range book.Groups {
		var platform, status, subscription, name string
		if err = tx.QueryRowContext(ctx, `SELECT platform,status,subscription_type,name FROM groups WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, g.GroupID).Scan(&platform, &status, &subscription, &name); err != nil {
			return nil, err
		}
		if platform != g.Platform || status != "active" || subscription == "subscription" {
			return nil, errors.New("marketplace group must be active, platform-matched and balance-funded")
		}
		var selfOwned bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups ag JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=$1 AND a.deleted_at IS NULL AND a.owner_user_id IS NULL)`, g.GroupID).Scan(&selfOwned); err != nil {
			return nil, err
		}
		if selfOwned {
			return nil, errors.New("marketplace group contains platform-owned accounts")
		}
		book.Groups[i].Name = name
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT greatest(coalesce((SELECT max(version) FROM marketplace_price_versions),0),coalesce((SELECT max(version) FROM marketplace_policies),0))+1`).Scan(&version); err != nil {
		return nil, err
	}
	body, err := json.Marshal(book)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_policies(version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, version, policy.FeeBPS, policy.HoldHours, policy.MaxSupplierConcurrency, policy.MinimumPayoutUSD.String(), actor, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_price_versions(version,rules,published_by,published_at) VALUES($1,$2,$3,$4)`, version, body, actor, now); err != nil {
		return nil, err
	}
	for _, g := range book.Groups {
		if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_group_registry(group_id,first_price_version) VALUES($1,$2) ON CONFLICT(group_id) DO NOTHING`, g.GroupID, version); err != nil {
			return nil, err
		}
	}
	notice := fmt.Sprintf("Marketplace policy version %d has been published. New shared requests use the new policy. Review the current marketplace terms and prices.", version)
	if _, err = tx.ExecContext(ctx, `INSERT INTO announcements(title,content,status,notify_mode,targeting,created_by,updated_by) VALUES('Marketplace policy updated',$1,'active','silent','{}'::jsonb,$2,$2)`, notice, actor); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	policy.Version, policy.PublishedBy, policy.PublishedAt = version, &actor, &now
	return &service.MarketplacePublishedCatalog{Version: version, PublishedAt: now, Policy: policy, Book: book}, nil
}

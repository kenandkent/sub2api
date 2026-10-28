package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

// ProjectMarketplaceCapture records a usage view and durable cache work, without
// invoking legacy billing. The caller commits these together with the outbox marker.
func ProjectMarketplaceCapture(ctx context.Context, tx *sql.Tx, e MarketplaceOutboxEvent) error {
	if e.EventType != "marketplace_captured" {
		return fmt.Errorf("unknown marketplace event %q", e.EventType)
	}
	var qdata, udata []byte
	var group int64
	var charge decimal.Decimal
	var created sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT a.quote,a.usage,r.group_id,c.consumer_charge,c.created_at FROM marketplace_captures c JOIN marketplace_attempts a ON a.id=c.attempt_id AND a.receipt_id=c.receipt_id JOIN marketplace_requests r ON r.id=c.receipt_id WHERE c.id=$1 AND c.receipt_id=$2`, e.CaptureID, e.ReceiptID).Scan(&qdata, &udata, &group, &charge, &created)
	if err != nil {
		return err
	}
	var q service.QuoteSnapshot
	var u service.UsageSnapshot
	if err = json.Unmarshal(qdata, &q); err != nil {
		return err
	}
	if err = json.Unmarshal(udata, &u); err != nil {
		return err
	}
	calculated, err := service.NewMarketplaceQuoteService().CalculateActual(q, u)
	if err != nil {
		return err
	}
	if !calculated.Equal(charge) {
		return errors.New("marketplace projected charge does not match capture")
	}
	if q.AccountRateMultiplier == nil {
		return errors.New("marketplace account rate missing")
	}
	tier := q.Tiers[0]
	for _, t := range q.Tiers[1:] {
		if u.InputTokens < t.FromTokens {
			break
		}
		tier = t
	}
	input := u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	cost := func(price *service.Money, n int64) decimal.Decimal { return price.Mul(decimal.NewFromInt(n)).Shift(-6) }
	ic, oc, rc, wc := cost(tier.InputPerMillion, input), cost(tier.OutputPerMillion, u.OutputTokens), cost(tier.CacheReadPerMillion, u.CacheReadTokens), cost(tier.CacheWritePerMillion, u.CacheWriteTokens)
	total := ic.Add(oc).Add(rc).Add(wc)
	// Normal usage rows store mutually exclusive input buckets and unmultiplied costs.
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,requested_model,upstream_model,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,input_cost,output_cost,cache_creation_cost,cache_read_cost,total_cost,actual_cost,rate_multiplier,billing_type,billing_mode,created_at,marketplace_receipt_id,marketplace_price_version,marketplace_policy_version,marketplace_rate_multiplier,marketplace_input_cost,marketplace_output_cost,marketplace_cache_read_cost,marketplace_cache_write_cost,marketplace_total_cost,account_rate_multiplier,marketplace_account_rate_multiplier) VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,0,'token',$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`, e.BuyerUserID, e.APIKeyID, e.AccountID, group, "marketplace:"+e.ReceiptID, q.PublicModel, q.ActualModel, input, u.OutputTokens, u.CacheWriteTokens, u.CacheReadTokens, ic.String(), oc.String(), wc.String(), rc.String(), total.String(), charge.String(), q.RateMultiplier.String(), created.Time, e.ReceiptID, q.PriceVersion, q.PolicyVersion, q.RateMultiplier.String(), ic.String(), oc.String(), rc.String(), wc.String(), total.String(), q.AccountRateMultiplier.String(), q.AccountRateMultiplier.String())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_cache_tasks(capture_id,user_id,api_key_id) VALUES($1,$2,$3)`, e.CaptureID, e.BuyerUserID, e.APIKeyID); err != nil {
		return err
	}
	// Existing durable auth-outbox worker propagates deletion and retries failures.
	// All user's keys can cache the user's balance, so invalidate every key.
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_cache_invalidation_outbox(cache_key) SELECT encode(sha256(convert_to(key,'UTF8')),'hex') FROM api_keys WHERE user_id=$1 AND key<>''`, e.BuyerUserID)
	return err
}

func NewMarketplaceOutboxWorker(db *sql.DB) *MarketplaceOutboxWorker {
	return &MarketplaceOutboxWorker{DB: db, Project: ProjectMarketplaceCapture}
}

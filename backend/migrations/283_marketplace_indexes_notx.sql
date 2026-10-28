CREATE INDEX CONCURRENTLY IF NOT EXISTS marketplace_captures_pending_maturity_idx ON marketplace_captures(matures_at,id) WHERE status='pending';
CREATE INDEX CONCURRENTLY IF NOT EXISTS marketplace_captures_owner_ledger_idx ON marketplace_captures(owner_user_id,ledger_transaction_id);

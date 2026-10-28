-- The recovery worker only examines unresolved receipts. Avoid scanning the
-- complete immutable request history every minute as settled rows accumulate.
CREATE INDEX marketplace_requests_stale_review
ON marketplace_requests (updated_at, id)
WHERE status IN ('prepared','budget_reserved','dispatched','usage_pending');

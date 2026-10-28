CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS usage_logs_marketplace_receipt_unique ON usage_logs(marketplace_receipt_id) WHERE marketplace_receipt_id IS NOT NULL;

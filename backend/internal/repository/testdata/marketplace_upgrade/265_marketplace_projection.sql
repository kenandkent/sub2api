-- Keep the financial receipt as the stable identity of a projected usage row.
ALTER TABLE usage_logs ADD COLUMN marketplace_receipt_id UUID REFERENCES marketplace_requests(id);
ALTER TABLE usage_logs ADD COLUMN marketplace_price_version BIGINT;
ALTER TABLE usage_logs ADD COLUMN marketplace_policy_version BIGINT;
ALTER TABLE usage_logs ADD COLUMN marketplace_rate_multiplier NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_account_rate_multiplier NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_input_cost NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_output_cost NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_cache_read_cost NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_cache_write_cost NUMERIC;
ALTER TABLE usage_logs ADD COLUMN marketplace_total_cost NUMERIC;
CREATE UNIQUE INDEX usage_logs_marketplace_receipt_unique ON usage_logs(marketplace_receipt_id) WHERE marketplace_receipt_id IS NOT NULL;

-- References only: no captured balances or credentials are copied here.
CREATE TABLE marketplace_cache_tasks (
 id BIGSERIAL PRIMARY KEY,
 capture_id BIGINT NOT NULL UNIQUE REFERENCES marketplace_captures(id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
 attempts INTEGER NOT NULL DEFAULT 0,
 delivery_stage INTEGER NOT NULL DEFAULT 0 CHECK(delivery_stage IN (0,1)),
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 claimed_at TIMESTAMPTZ,
 claim_token TEXT,
 processed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_cache_tasks_ready ON marketplace_cache_tasks(available_at,id) WHERE processed_at IS NULL;

-- Durable invalidation for every explicit buyer funding projection mutation.
CREATE TABLE marketplace_balance_invalidation_tasks (
 id BIGSERIAL PRIMARY KEY,
 user_id BIGINT NOT NULL REFERENCES users(id),
 attempts INTEGER NOT NULL DEFAULT 0,
 delivery_stage INTEGER NOT NULL DEFAULT 0 CHECK (delivery_stage IN (0,1)),
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 claimed_at TIMESTAMPTZ,
 claim_token TEXT,
 processed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_balance_invalidation_ready ON marketplace_balance_invalidation_tasks(available_at,id) WHERE processed_at IS NULL;

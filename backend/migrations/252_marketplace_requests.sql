-- Receipts and attempts never contain prompts or credentials. Keep financial
-- history when a user, key, group, or account is later deactivated.
CREATE TABLE marketplace_requests (
 id UUID PRIMARY KEY,
 buyer_user_id BIGINT NOT NULL REFERENCES users(id),
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
 group_id BIGINT NOT NULL REFERENCES groups(id),
 request_digest TEXT NOT NULL CHECK (request_digest <> ''),
 idempotency_key TEXT CHECK (idempotency_key IS NULL OR idempotency_key <> ''),
 status TEXT NOT NULL DEFAULT 'prepared' CHECK (status IN ('prepared','budget_reserved','dispatched','usage_pending','needs_review','settled','voided')),
 budget NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (budget >= 0),
 actual_charge NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (actual_charge >= 0),
 billable_attempt_id BIGINT,
 review_reason TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX marketplace_requests_idempotency ON marketplace_requests (buyer_user_id,api_key_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE TABLE marketplace_attempts (
 id BIGSERIAL PRIMARY KEY,
 receipt_id UUID NOT NULL REFERENCES marketplace_requests(id),
 ordinal INTEGER NOT NULL CHECK (ordinal > 0),
 account_id BIGINT NOT NULL REFERENCES accounts(id),
 offer_id BIGINT NOT NULL,
 owner_user_id BIGINT NOT NULL REFERENCES users(id),
 quote JSONB NOT NULL,
 quote_digest TEXT NOT NULL CHECK (quote_digest <> ''),
 status TEXT NOT NULL CHECK (status IN ('prepared','possibly_executed','definitely_not_dispatched','rejected_before_execution','usage_recorded','settled')),
 usage JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE (receipt_id,ordinal),
 UNIQUE (receipt_id,id)
);
ALTER TABLE marketplace_requests ADD CONSTRAINT marketplace_requests_billable_attempt_fk FOREIGN KEY (id,billable_attempt_id) REFERENCES marketplace_attempts (receipt_id,id);
CREATE UNIQUE INDEX marketplace_attempts_one_active ON marketplace_attempts (receipt_id) WHERE status IN ('prepared','possibly_executed','usage_recorded','settled');
CREATE FUNCTION marketplace_attempt_snapshot_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.receipt_id, NEW.ordinal, NEW.account_id, NEW.offer_id, NEW.owner_user_id, NEW.quote, NEW.quote_digest)
    IS DISTINCT FROM (OLD.receipt_id, OLD.ordinal, OLD.account_id, OLD.offer_id, OLD.owner_user_id, OLD.quote, OLD.quote_digest) THEN
   RAISE EXCEPTION 'marketplace attempt snapshot is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER marketplace_attempt_snapshot_immutable BEFORE UPDATE ON marketplace_attempts
 FOR EACH ROW EXECUTE FUNCTION marketplace_attempt_snapshot_immutable();

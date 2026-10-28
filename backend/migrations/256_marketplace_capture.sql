ALTER TABLE sharing_offers ADD CONSTRAINT sharing_offers_identity_key UNIQUE (id, account_id, owner_user_id);
ALTER TABLE marketplace_attempts ADD CONSTRAINT marketplace_attempt_offer_identity_fk FOREIGN KEY (offer_id, account_id, owner_user_id) REFERENCES sharing_offers(id, account_id, owner_user_id);
CREATE TABLE marketplace_captures (
 id BIGSERIAL PRIMARY KEY,
 receipt_id UUID NOT NULL UNIQUE REFERENCES marketplace_requests(id),
 attempt_id BIGINT NOT NULL UNIQUE,
 owner_user_id BIGINT NOT NULL REFERENCES users(id),
 consumer_charge NUMERIC(20,8) NOT NULL CHECK(consumer_charge >= 0),
 supplier_net NUMERIC(20,8) NOT NULL CHECK(supplier_net >= 0),
 platform_fee NUMERIC(20,8) NOT NULL CHECK(platform_fee >= 0),
 supplier_debt_offset NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(supplier_debt_offset >= 0 AND supplier_debt_offset <= supplier_net),
 refunded_charge NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(refunded_charge >= 0 AND refunded_charge <= consumer_charge),
 refunded_net NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(refunded_net >= 0 AND refunded_net <= supplier_net),
 refunded_fee NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(refunded_fee >= 0 AND refunded_fee <= platform_fee),
 matures_at TIMESTAMPTZ NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','matured','reversed')),
 ledger_transaction_id BIGINT REFERENCES ledger_transactions(id),
 fingerprint TEXT NOT NULL,
 api_key_quota_exhausted BOOLEAN NOT NULL DEFAULT FALSE,
 api_key_rate_limit_changed BOOLEAN NOT NULL DEFAULT FALSE,
 buyer_balance_after NUMERIC(20,8) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(receipt_id,attempt_id) REFERENCES marketplace_attempts(receipt_id,id),
 CHECK(consumer_charge = supplier_net + platform_fee),
 CHECK(refunded_charge = refunded_net + refunded_fee)
);
CREATE TABLE marketplace_outbox (
 id BIGSERIAL PRIMARY KEY,
 event_id TEXT NOT NULL UNIQUE,
 event_type TEXT NOT NULL,
 receipt_id UUID NOT NULL REFERENCES marketplace_requests(id),
 capture_id BIGINT NOT NULL REFERENCES marketplace_captures(id),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 processed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_outbox_ready ON marketplace_outbox(next_attempt_at,id) WHERE processed_at IS NULL;

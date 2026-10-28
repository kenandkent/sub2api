-- Every migrated top-up refund is reserved against one verified cash origin.
CREATE TABLE marketplace_payment_refund_attempts (
 attempt_id text PRIMARY KEY,
 order_id bigint NOT NULL,
 user_id bigint NOT NULL REFERENCES users(id),
 lot_id bigint NOT NULL REFERENCES wallet_funding_lots(id),
 amount numeric(20,8) NOT NULL CHECK(amount > 0),
 state text NOT NULL CHECK(state IN ('reserved','confirmed','released')),
 provider_refund_id text UNIQUE,
 needs_review boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_payment_refund_order_idx ON marketplace_payment_refund_attempts(order_id);

CREATE TABLE marketplace_payment_risk_flags (
 user_id bigint PRIMARY KEY REFERENCES users(id),
 reason text NOT NULL CHECK(reason<>''),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE marketplace_payment_chargebacks (
 event_id text PRIMARY KEY,
 order_id bigint NOT NULL,
 user_id bigint NOT NULL REFERENCES users(id),
 actor_id bigint NOT NULL CHECK(actor_id>0),
 amount numeric(20,8) NOT NULL CHECK(amount>0),
 revoked numeric(20,8) NOT NULL CHECK(revoked>=0),
 buyer_debt numeric(20,8) NOT NULL CHECK(buyer_debt>=0 AND revoked+buyer_debt=amount),
 evidence text NOT NULL CHECK(evidence<>''),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_payment_chargeback_order_idx ON marketplace_payment_chargebacks(order_id);

CREATE TABLE marketplace_payment_refund_resolutions (
 operation_id text PRIMARY KEY,
 attempt_id text NOT NULL UNIQUE REFERENCES marketplace_payment_refund_attempts(attempt_id),
 order_id bigint NOT NULL,
 user_id bigint NOT NULL REFERENCES users(id),
 actor_id bigint NOT NULL CHECK(actor_id>0),
 decision text NOT NULL CHECK(decision IN ('confirmed_refunded','confirmed_failed')),
 provider_refund_id text UNIQUE,
 evidence text NOT NULL CHECK(evidence<>''),
 amount numeric(20,8) NOT NULL CHECK(amount>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(decision<>'confirmed_refunded' OR (provider_refund_id IS NOT NULL AND provider_refund_id<>''))
);

CREATE FUNCTION marketplace_payment_refund_resolution_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'payment refund resolutions are immutable';
END;
$$;
CREATE TRIGGER marketplace_payment_refund_resolution_immutable
 BEFORE UPDATE OR DELETE ON marketplace_payment_refund_resolutions
 FOR EACH ROW EXECUTE FUNCTION marketplace_payment_refund_resolution_immutable();

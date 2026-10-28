-- Version each risk occurrence monotonically, including a new chargeback after
-- an earlier flag was reviewed and cleared.
CREATE SEQUENCE marketplace_funding_risk_version_seq;
ALTER TABLE marketplace_payment_risk_flags ADD COLUMN version BIGINT NOT NULL DEFAULT nextval('marketplace_funding_risk_version_seq');
CREATE TABLE marketplace_funding_admin_operations (
 operation_id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('reclassify','clear_risk')),
 fingerprint TEXT NOT NULL,
 actor_id BIGINT NOT NULL REFERENCES users(id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 order_id BIGINT REFERENCES payment_orders(id),
 amount NUMERIC(20,8), cash_amount NUMERIC(20,8), bonus_amount NUMERIC(20,8),
 risk_version BIGINT, prior_reason TEXT,
 evidence TEXT NOT NULL CHECK(length(trim(evidence))>0 AND length(evidence)<=4000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX marketplace_funding_admin_order_idx ON marketplace_funding_admin_operations(order_id) WHERE kind='reclassify';
CREATE FUNCTION marketplace_funding_admin_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'funding admin operations are immutable'; END $$;
CREATE TRIGGER marketplace_funding_admin_immutable BEFORE UPDATE OR DELETE ON marketplace_funding_admin_operations FOR EACH ROW EXECUTE FUNCTION marketplace_funding_admin_immutable();

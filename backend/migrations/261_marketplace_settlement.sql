-- Refund records are append-only; original capture and ledger links remain intact.
CREATE TABLE marketplace_refunds (
 id BIGSERIAL PRIMARY KEY,
 operation_id TEXT NOT NULL UNIQUE CHECK(operation_id<>''),
 receipt_id UUID NOT NULL REFERENCES marketplace_requests(id),
 actor_user_id BIGINT NOT NULL REFERENCES users(id),
 amount NUMERIC(20,8) NOT NULL CHECK(amount>0),
 supplier_amount NUMERIC(20,8) NOT NULL CHECK(supplier_amount>=0),
 platform_amount NUMERIC(20,8) NOT NULL CHECK(platform_amount>=0),
 ledger_transaction_id BIGINT NOT NULL REFERENCES ledger_transactions(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(amount=supplier_amount+platform_amount)
);
CREATE INDEX marketplace_refunds_receipt_idx ON marketplace_refunds(receipt_id,id);
CREATE FUNCTION marketplace_refunds_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'marketplace refunds are immutable'; END $$;
CREATE TRIGGER marketplace_refunds_immutable BEFORE UPDATE OR DELETE ON marketplace_refunds FOR EACH ROW EXECUTE FUNCTION marketplace_refunds_immutable();

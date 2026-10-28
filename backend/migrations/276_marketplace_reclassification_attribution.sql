-- Preserve the verified fulfillment trail in the immutable reclassification audit.
ALTER TABLE marketplace_funding_admin_operations
 ADD COLUMN verified_redeem_id BIGINT REFERENCES redeem_codes(id) ON DELETE RESTRICT,
 ADD COLUMN verified_redeem_code TEXT,
 ADD COLUMN verified_completed_at TIMESTAMPTZ,
 ADD COLUMN verified_paid_at TIMESTAMPTZ,
 ADD COLUMN verified_redeemed_at TIMESTAMPTZ,
 ADD COLUMN remaining_provenance_confirmed BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN legacy_refund_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(legacy_refund_amount>=0);

-- Separate chargeback portions already covered by classified source lots from
-- previously unclassified funding, so the lifetime order cap counts each once.
CREATE TABLE marketplace_chargeback_reclass_attributions (
 event_id TEXT PRIMARY KEY REFERENCES marketplace_payment_chargebacks(event_id) ON DELETE RESTRICT,
 cash_amount NUMERIC(20,8) NOT NULL CHECK(cash_amount>=0),
 bonus_amount NUMERIC(20,8) NOT NULL CHECK(bonus_amount>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER marketplace_chargeback_reclass_attribution_immutable
 BEFORE UPDATE OR DELETE ON marketplace_chargeback_reclass_attributions
 FOR EACH ROW EXECUTE FUNCTION marketplace_funding_admin_immutable();

-- Refund attribution is captured at reservation and remains immutable even
-- if the provider later fails; the attempt state decides whether it counts.
CREATE TABLE marketplace_refund_reclass_attributions (
 attempt_id TEXT PRIMARY KEY REFERENCES marketplace_payment_refund_attempts(attempt_id) ON DELETE RESTRICT,
 cash_amount NUMERIC(20,8) NOT NULL CHECK(cash_amount>=0),
 bonus_amount NUMERIC(20,8) NOT NULL CHECK(bonus_amount>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER marketplace_refund_reclass_attribution_immutable
 BEFORE UPDATE OR DELETE ON marketplace_refund_reclass_attributions
 FOR EACH ROW EXECUTE FUNCTION marketplace_funding_admin_immutable();

-- One credited redeem can prove at most one original order, even if imported
-- historical payment orders accidentally reuse the same recharge code.
CREATE TABLE marketplace_funding_redeem_bindings (
 redeem_id BIGINT PRIMARY KEY REFERENCES redeem_codes(id) ON DELETE RESTRICT,
 order_id BIGINT NOT NULL UNIQUE REFERENCES payment_orders(id) ON DELETE RESTRICT,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER marketplace_funding_redeem_binding_immutable
 BEFORE UPDATE OR DELETE ON marketplace_funding_redeem_bindings
 FOR EACH ROW EXECUTE FUNCTION marketplace_funding_admin_immutable();

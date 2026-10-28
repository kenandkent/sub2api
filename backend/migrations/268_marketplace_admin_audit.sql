-- Historical refunds predate operator reasons; new administrative refunds require one.
ALTER TABLE marketplace_refunds ADD COLUMN reason TEXT NOT NULL DEFAULT '';

-- Freeze decisions are durable operation records, including actor, reason and result.
CREATE FUNCTION supplier_payout_freeze_audit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.kind = 'freeze' THEN RAISE EXCEPTION 'payout freeze audit is immutable'; END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_payout_freeze_audit_immutable BEFORE UPDATE OR DELETE ON supplier_payout_operations FOR EACH ROW EXECUTE FUNCTION supplier_payout_freeze_audit_guard();

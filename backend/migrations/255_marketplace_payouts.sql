CREATE TABLE supplier_payout_destinations (
 id bigserial PRIMARY KEY, supplier_id bigint NOT NULL REFERENCES users(id), masked_label text NOT NULL CHECK(masked_label<>''), version bigint NOT NULL CHECK(version>0),
 credential jsonb NOT NULL, verified boolean NOT NULL DEFAULT false, verified_by bigint REFERENCES users(id), verified_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(supplier_id,version), CHECK ((verified_by IS NULL) = (verified_at IS NULL)), CHECK (verified = (verified_by IS NOT NULL))
);
CREATE INDEX supplier_payout_destinations_owner_idx ON supplier_payout_destinations(supplier_id,id);
CREATE TABLE supplier_payouts (
 id bigserial PRIMARY KEY, supplier_id bigint NOT NULL REFERENCES users(id), destination_id bigint NOT NULL REFERENCES supplier_payout_destinations(id),
 masked_label text NOT NULL, amount numeric(20,8) NOT NULL CHECK(amount>0 AND amount=round(amount,2)), currency text NOT NULL DEFAULT 'USD' CHECK(currency='USD'),
 status text NOT NULL CHECK(status IN ('requested','approved','processing','paid','rejected','cancelled','failed_confirmed','risk_cancelled')),
 external_reference text UNIQUE, evidence text, paid_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(status<>'paid' OR (external_reference IS NOT NULL AND evidence IS NOT NULL AND paid_at IS NOT NULL))
);
CREATE INDEX supplier_payouts_owner_idx ON supplier_payouts(supplier_id,id);
CREATE TABLE supplier_payout_operations (
 operation_id text PRIMARY KEY, fingerprint text NOT NULL, kind text NOT NULL, result_id bigint NOT NULL,
 actor_id bigint NOT NULL REFERENCES users(id), result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION supplier_payout_destination_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.supplier_id<>OLD.supplier_id OR NEW.masked_label<>OLD.masked_label OR NEW.version<>OLD.version OR NEW.credential<>OLD.credential OR NEW.created_at<>OLD.created_at OR OLD.verified THEN
  RAISE EXCEPTION 'payout destination version is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_payout_destination_immutable BEFORE UPDATE OR DELETE ON supplier_payout_destinations FOR EACH ROW EXECUTE FUNCTION supplier_payout_destination_guard();
CREATE FUNCTION supplier_payout_snapshot_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.supplier_id<>OLD.supplier_id OR NEW.destination_id<>OLD.destination_id OR NEW.masked_label<>OLD.masked_label OR NEW.amount<>OLD.amount OR NEW.currency<>OLD.currency OR NEW.created_at<>OLD.created_at THEN
  RAISE EXCEPTION 'payout snapshot is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_payout_snapshot_immutable BEFORE UPDATE OR DELETE ON supplier_payouts FOR EACH ROW EXECUTE FUNCTION supplier_payout_snapshot_guard();

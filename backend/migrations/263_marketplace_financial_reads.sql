CREATE TABLE supplier_statements (
 id bigserial PRIMARY KEY, supplier_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 month date NOT NULL CHECK (month = date_trunc('month', month)::date), currency text NOT NULL DEFAULT 'USD' CHECK(currency='USD'), version integer NOT NULL CHECK(version>0), cutoff_ledger_id bigint NOT NULL,
 opening_pending numeric(20,8) NOT NULL, opening_available numeric(20,8) NOT NULL, opening_reserved numeric(20,8) NOT NULL,
 closing_pending numeric(20,8) NOT NULL, closing_available numeric(20,8) NOT NULL, closing_reserved numeric(20,8) NOT NULL,
 earned_net numeric(20,8) NOT NULL, platform_fee numeric(20,8) NOT NULL, refund_net numeric(20,8) NOT NULL, payout_paid numeric(20,8) NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(supplier_id,month,version), UNIQUE(supplier_id,month)
);
CREATE FUNCTION supplier_statement_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'supplier statements are immutable'; END $$;
CREATE TRIGGER supplier_statement_immutable BEFORE UPDATE OR DELETE ON supplier_statements FOR EACH ROW EXECUTE FUNCTION supplier_statement_immutable();
CREATE TABLE supplier_statement_generation_operations (
 operation_id text PRIMARY KEY, actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT, supplier_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 month date NOT NULL, reason text NOT NULL CHECK(length(trim(reason))>0), statement_id bigint NOT NULL REFERENCES supplier_statements(id) ON DELETE RESTRICT, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER supplier_statement_generation_immutable BEFORE UPDATE OR DELETE ON supplier_statement_generation_operations FOR EACH ROW EXECUTE FUNCTION supplier_statement_immutable();
CREATE TABLE supplier_statement_entries (
 id bigserial PRIMARY KEY, statement_id bigint NOT NULL REFERENCES supplier_statements(id) ON DELETE RESTRICT,
 operation_id text NOT NULL, entry_date timestamptz NOT NULL, account_id bigint NOT NULL, account_name text NOT NULL, model text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('capture','refund')), consumer_charge numeric(20,8) NOT NULL, platform_fee numeric(20,8) NOT NULL, supplier_net numeric(20,8) NOT NULL,
 UNIQUE(statement_id,operation_id)
);
CREATE TRIGGER supplier_statement_entries_immutable BEFORE UPDATE OR DELETE ON supplier_statement_entries FOR EACH ROW EXECUTE FUNCTION supplier_statement_immutable();
-- A booking month is assigned by the database at insertion, under a shared
-- period lock. Closing takes the matching exclusive lock before reading.
ALTER TABLE ledger_transactions ADD COLUMN booked_at timestamptz;
ALTER TABLE ledger_transactions DISABLE TRIGGER ledger_transactions_immutable;
UPDATE ledger_transactions SET booked_at=created_at;
ALTER TABLE ledger_transactions ENABLE TRIGGER ledger_transactions_immutable;
ALTER TABLE ledger_transactions ALTER COLUMN booked_at SET NOT NULL;
CREATE FUNCTION marketplace_book_ledger_period() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE booked timestamptz; after_lock timestamptz; period text;
BEGIN
 LOOP
  booked := clock_timestamp();
  period := to_char(booked AT TIME ZONE 'UTC','YYYY-MM');
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('marketplace-period:'||period,0));
  after_lock := clock_timestamp();
  EXIT WHEN to_char(after_lock AT TIME ZONE 'UTC','YYYY-MM') = period;
 END LOOP;
 NEW.booked_at := after_lock;
 RETURN NEW;
END $$;
CREATE TRIGGER marketplace_ledger_booking_period BEFORE INSERT ON ledger_transactions FOR EACH ROW EXECUTE FUNCTION marketplace_book_ledger_period();
CREATE OR REPLACE FUNCTION marketplace_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='ledger_entries' THEN
   IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'ledger entries are immutable'; END IF;
   IF (SELECT sealed FROM ledger_transactions WHERE id=NEW.transaction_id FOR UPDATE) THEN RAISE EXCEPTION 'ledger transaction is sealed'; END IF;
   RETURN NEW;
 END IF;
 IF TG_OP='DELETE' OR OLD.sealed OR NEW.operation_id<>OLD.operation_id OR NEW.actor<>OLD.actor OR NEW.currency<>OLD.currency OR NEW.fingerprint<>OLD.fingerprint OR NEW.id<>OLD.id OR NEW.original_transaction_id IS DISTINCT FROM OLD.original_transaction_id OR NEW.created_at<>OLD.created_at OR NEW.booked_at<>OLD.booked_at OR NOT NEW.sealed THEN
   RAISE EXCEPTION 'ledger transactions are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE INDEX ledger_transactions_booked_at_id_idx ON ledger_transactions(booked_at,id);
CREATE INDEX ledger_entries_supplier_subject_idx ON ledger_entries(subject_id,transaction_id) WHERE account IN ('supplier_pending','supplier_available','supplier_payout_reserved','supplier_debt','supplier_paid');

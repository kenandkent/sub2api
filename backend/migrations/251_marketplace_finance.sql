CREATE TABLE wallet_funding_states (
 id bigserial PRIMARY KEY, user_id bigint NOT NULL UNIQUE REFERENCES users(id), debt numeric(20,8) NOT NULL DEFAULT 0 CHECK(debt>=0),
 migration_version integer NOT NULL DEFAULT 0, reconciled_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_funding_lots (
 id bigserial PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id), kind text NOT NULL CHECK(kind IN ('cash_paid','promotional','legacy_unverified','platform_subsidy')),
 origin_type text NOT NULL, origin_id text NOT NULL, currency text NOT NULL DEFAULT 'USD' CHECK(currency='USD'),
 original numeric(20,8) NOT NULL CHECK(original>=0), remaining numeric(20,8) NOT NULL CHECK(remaining>=0),
 reserved numeric(20,8) NOT NULL DEFAULT 0 CHECK(reserved>=0 AND remaining>=reserved), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(origin_type,origin_id)
);
CREATE INDEX wallet_funding_lots_user_id_idx ON wallet_funding_lots(user_id,id);
CREATE TABLE wallet_funding_operations (
 operation_id text PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id), actor text NOT NULL CHECK(actor<>''),
 operation_type text NOT NULL, fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_funding_allocations (
 id bigserial PRIMARY KEY, lot_id bigint NOT NULL REFERENCES wallet_funding_lots(id), hold_operation_id text NOT NULL REFERENCES wallet_funding_operations(operation_id),
 reserved numeric(20,8) NOT NULL CHECK(reserved>0), captured numeric(20,8) NOT NULL DEFAULT 0 CHECK(captured>=0),
 released numeric(20,8) NOT NULL DEFAULT 0 CHECK(released>=0), refunded numeric(20,8) NOT NULL DEFAULT 0 CHECK(refunded>=0),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(hold_operation_id,lot_id), CHECK(captured+released<=reserved), CHECK(refunded<=captured)
);
CREATE TABLE supplier_wallets (
 id bigserial PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id), currency text NOT NULL DEFAULT 'USD' CHECK(currency='USD'),
 pending numeric(20,8) NOT NULL DEFAULT 0 CHECK(pending>=0), available numeric(20,8) NOT NULL DEFAULT 0 CHECK(available>=0),
 payout_reserved numeric(20,8) NOT NULL DEFAULT 0 CHECK(payout_reserved>=0), debt numeric(20,8) NOT NULL DEFAULT 0 CHECK(debt>=0),
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(user_id,currency)
);
CREATE TABLE ledger_transactions (
 id bigserial PRIMARY KEY, operation_id text NOT NULL UNIQUE, actor text NOT NULL CHECK(actor<>''), currency text NOT NULL CHECK(currency='USD'), fingerprint text NOT NULL,
 original_transaction_id bigint REFERENCES ledger_transactions(id), sealed boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ledger_entries (
 id bigserial PRIMARY KEY, transaction_id bigint NOT NULL REFERENCES ledger_transactions(id), account text NOT NULL CHECK(account<>''),
 subject_id bigint, side text NOT NULL CHECK(side IN ('debit','credit')), amount numeric(20,8) NOT NULL CHECK(amount>0), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION marketplace_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='ledger_entries' THEN
   IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'ledger entries are immutable'; END IF;
   IF (SELECT sealed FROM ledger_transactions WHERE id=NEW.transaction_id FOR UPDATE) THEN RAISE EXCEPTION 'ledger transaction is sealed'; END IF;
   RETURN NEW;
 END IF;
 IF TG_OP='DELETE' OR OLD.sealed OR NEW.operation_id<>OLD.operation_id OR NEW.actor<>OLD.actor OR NEW.currency<>OLD.currency OR NEW.fingerprint<>OLD.fingerprint OR NEW.id<>OLD.id OR NEW.original_transaction_id IS DISTINCT FROM OLD.original_transaction_id OR NEW.created_at<>OLD.created_at OR NOT NEW.sealed THEN
   RAISE EXCEPTION 'ledger transactions are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ledger_entries_immutable BEFORE INSERT OR UPDATE OR DELETE ON ledger_entries FOR EACH ROW EXECUTE FUNCTION marketplace_ledger_guard();
CREATE TRIGGER ledger_transactions_immutable BEFORE UPDATE OR DELETE ON ledger_transactions FOR EACH ROW EXECUTE FUNCTION marketplace_ledger_guard();
CREATE FUNCTION marketplace_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE txid bigint; sealed_value boolean; entry_count bigint; net numeric;
BEGIN
 IF TG_TABLE_NAME='ledger_transactions' THEN txid := NEW.id; ELSE txid := NEW.transaction_id; END IF;
 SELECT sealed INTO sealed_value FROM ledger_transactions WHERE id=txid;
 SELECT count(*),coalesce(sum(CASE side WHEN 'debit' THEN amount ELSE -amount END),0) INTO entry_count,net FROM ledger_entries WHERE transaction_id=txid;
 IF NOT sealed_value OR entry_count<2 OR net<>0 THEN RAISE EXCEPTION 'ledger transaction % is unsealed or unbalanced',txid; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER ledger_transactions_balanced AFTER INSERT OR UPDATE ON ledger_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION marketplace_ledger_balanced();
CREATE CONSTRAINT TRIGGER ledger_entries_balanced AFTER INSERT ON ledger_entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION marketplace_ledger_balanced();

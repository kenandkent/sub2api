-- Cover both explicit funding writers and the legacy balance bridge. Queue in
-- the same transaction so a rollback never invalidates an uncommitted value.
CREATE FUNCTION marketplace_queue_buyer_balance_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.balance IS NOT DISTINCT FROM OLD.balance AND NEW.frozen_balance IS NOT DISTINCT FROM OLD.frozen_balance THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM wallet_funding_states WHERE user_id=NEW.id) THEN RETURN NEW; END IF;
 INSERT INTO marketplace_balance_invalidation_tasks(user_id) VALUES(NEW.id);
 INSERT INTO auth_cache_invalidation_outbox(cache_key)
 SELECT encode(sha256(convert_to(key,'UTF8')),'hex') FROM api_keys WHERE user_id=NEW.id AND key<>'';
 RETURN NEW;
END $$;
CREATE TRIGGER marketplace_buyer_balance_invalidation AFTER UPDATE OF balance,frozen_balance ON users
 FOR EACH ROW EXECUTE FUNCTION marketplace_queue_buyer_balance_invalidation();

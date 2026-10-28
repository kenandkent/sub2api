CREATE TABLE marketplace_vault_rotation_operations (
 operation_id TEXT PRIMARY KEY CHECK(length(operation_id) BETWEEN 1 AND 255),
 actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 4096),
 target_key_id TEXT NOT NULL CHECK(length(target_key_id)>0),
 batch_limit INTEGER NOT NULL CHECK(batch_limit BETWEEN 1 AND 200),
 status TEXT NOT NULL CHECK(status IN ('processing','completed')),
 processed INTEGER, remaining INTEGER, completed BOOLEAN,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK ((status='processing' AND processed IS NULL AND remaining IS NULL AND completed IS NULL)
     OR (status='completed' AND processed IS NOT NULL AND remaining IS NOT NULL AND completed IS NOT NULL AND processed>=0 AND remaining>=0 AND completed=(remaining=0)))
);

CREATE FUNCTION marketplace_vault_rotation_audit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status <> 'processing' OR NEW.status <> 'completed'
    OR NEW.operation_id<>OLD.operation_id OR NEW.actor_user_id<>OLD.actor_user_id
    OR NEW.reason<>OLD.reason OR NEW.target_key_id<>OLD.target_key_id
    OR NEW.batch_limit<>OLD.batch_limit OR NEW.created_at<>OLD.created_at THEN
   RAISE EXCEPTION 'vault rotation audit is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER marketplace_vault_rotation_audit_immutable BEFORE UPDATE OR DELETE ON marketplace_vault_rotation_operations
 FOR EACH ROW EXECUTE FUNCTION marketplace_vault_rotation_audit_guard();

CREATE OR REPLACE FUNCTION supplier_payout_destination_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rotation_id TEXT;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'payout destination version is immutable'; END IF;
 rotation_id := current_setting('marketplace.vault_rotation_operation', true);
 IF rotation_id IS NOT NULL AND rotation_id<>'' AND NEW.credential IS DISTINCT FROM OLD.credential
    AND (to_jsonb(NEW)-'credential') = (to_jsonb(OLD)-'credential')
    AND EXISTS(SELECT 1 FROM marketplace_vault_rotation_operations WHERE operation_id=rotation_id AND status='processing'
               AND target_key_id=NEW.credential->>'key_id' AND NEW.credential->>'version'='1') THEN
   RETURN NEW;
 END IF;
 IF NEW.supplier_id<>OLD.supplier_id OR NEW.masked_label<>OLD.masked_label OR NEW.version<>OLD.version
    OR NEW.credential<>OLD.credential OR NEW.created_at<>OLD.created_at OR OLD.verified THEN
   RAISE EXCEPTION 'payout destination version is immutable';
 END IF;
 RETURN NEW;
END $$;

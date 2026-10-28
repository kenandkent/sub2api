-- Operator decisions are immutable and share the settlement transaction.
CREATE TABLE marketplace_recovery_audits (
 operation_id TEXT PRIMARY KEY CHECK (length(operation_id) BETWEEN 1 AND 255),
 actor_user_id BIGINT NOT NULL REFERENCES users(id),
 receipt_id UUID NOT NULL REFERENCES marketplace_requests(id),
 action TEXT NOT NULL CHECK (action IN ('release_confirmed_unexecuted','retry_known_usage')),
 evidence TEXT NOT NULL CHECK (length(trim(evidence)) > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE FUNCTION marketplace_recovery_audit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'marketplace recovery audits are immutable'; END $$;
CREATE TRIGGER marketplace_recovery_audit_immutable BEFORE UPDATE OR DELETE ON marketplace_recovery_audits
 FOR EACH ROW EXECUTE FUNCTION marketplace_recovery_audit_immutable();

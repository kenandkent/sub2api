-- Incident reports are operator assertions for manual review; they do not
-- establish the provenance of usage or authorize financial compensation.
CREATE TABLE marketplace_execution_incidents (
 operation_id text PRIMARY KEY CHECK (octet_length(operation_id) BETWEEN 1 AND 255),
 receipt_id uuid NOT NULL REFERENCES marketplace_requests(id),
 attempt_id bigint NOT NULL,
 release_operation_id text NOT NULL REFERENCES marketplace_recovery_audits(operation_id),
 actor_user_id bigint NOT NULL REFERENCES users(id),
 quote_digest text NOT NULL CHECK (quote_digest <> ''),
 evidence text NOT NULL CHECK (octet_length(evidence) BETWEEN 1 AND 4000 AND length(trim(evidence)) > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (receipt_id,attempt_id) REFERENCES marketplace_attempts(receipt_id,id)
);
CREATE INDEX marketplace_execution_incidents_receipt_idx ON marketplace_execution_incidents(receipt_id,created_at,operation_id);
CREATE FUNCTION marketplace_execution_incident_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'marketplace execution incidents are immutable'; END $$;
CREATE TRIGGER marketplace_execution_incident_immutable BEFORE UPDATE OR DELETE ON marketplace_execution_incidents
 FOR EACH ROW EXECUTE FUNCTION marketplace_execution_incident_immutable();

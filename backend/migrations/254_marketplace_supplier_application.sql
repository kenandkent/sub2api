ALTER TABLE supplier_profiles ADD COLUMN application_fields JSONB NOT NULL DEFAULT '{}'::jsonb;
CREATE TABLE supplier_review_audits (
    id BIGSERIAL PRIMARY KEY,
    supplier_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    admin_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    previous_status VARCHAR(20) NOT NULL,
    new_status VARCHAR(20) NOT NULL,
    reason TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE FUNCTION supplier_review_audits_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'supplier review audits are immutable';
END;
$$;
CREATE TRIGGER supplier_review_audits_no_mutation BEFORE UPDATE OR DELETE ON supplier_review_audits
    FOR EACH ROW EXECUTE FUNCTION supplier_review_audits_immutable();

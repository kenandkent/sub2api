CREATE TABLE supplier_operations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operation_key TEXT NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 200),
    kind TEXT NOT NULL CHECK (kind IN ('account_create', 'credentials', 'authorization')),
    request_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','processing','completed','failed','needs_review')),
    account_id BIGINT REFERENCES accounts(id) ON DELETE RESTRICT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, operation_key)
);

CREATE TABLE sharing_offer_review_audits (
    id BIGSERIAL PRIMARY KEY,
    offer_id BIGINT NOT NULL REFERENCES sharing_offers(id) ON DELETE RESTRICT,
    admin_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    previous_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER sharing_offer_review_audits_no_mutation BEFORE UPDATE OR DELETE ON sharing_offer_review_audits
    FOR EACH ROW EXECUTE FUNCTION supplier_review_audits_immutable();

CREATE FUNCTION marketplace_account_owner_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.owner_user_id IS DISTINCT FROM NEW.owner_user_id THEN
        RAISE EXCEPTION 'account ownership is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER marketplace_account_owner_immutable BEFORE UPDATE OF owner_user_id ON accounts
    FOR EACH ROW EXECUTE FUNCTION marketplace_account_owner_immutable();

-- The nullable owner and both indexes are prepared by 249. Attaching the
-- prebuilt unique index avoids scanning accounts under an exclusive lock.
ALTER TABLE accounts ADD CONSTRAINT accounts_id_owner_user_id_key UNIQUE USING INDEX accounts_id_owner_user_id_key;

CREATE TABLE supplier_profiles (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','rejected','suspended')),
    review_reason TEXT NOT NULL DEFAULT '',
    agreement_version BIGINT,
    agreement_accepted_at TIMESTAMPTZ,
    payout_frozen BOOLEAN NOT NULL DEFAULT FALSE,
    risk_reason TEXT NOT NULL DEFAULT '',
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE sharing_offers (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL UNIQUE,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    review_status VARCHAR(20) NOT NULL DEFAULT 'draft' CHECK (review_status IN ('draft','pending_review','approved','rejected','revoked')),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    allowed_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT sharing_offers_account_owner_fk FOREIGN KEY (account_id, owner_user_id)
        REFERENCES accounts(id, owner_user_id) ON DELETE RESTRICT
);
CREATE INDEX sharing_offers_owner_user_id_idx ON sharing_offers(owner_user_id);

CREATE TABLE marketplace_policies (
    id BIGSERIAL PRIMARY KEY,
    version BIGINT NOT NULL UNIQUE CHECK (version > 0),
    fee_bps INTEGER NOT NULL CHECK (fee_bps BETWEEN 0 AND 10000),
    hold_hours INTEGER NOT NULL CHECK (hold_hours BETWEEN 0 AND 87600),
    max_supplier_concurrency INTEGER NOT NULL CHECK (max_supplier_concurrency > 0),
    minimum_payout_usd NUMERIC(20,8) NOT NULL CHECK (minimum_payout_usd >= 0 AND minimum_payout_usd = round(minimum_payout_usd, 2)),
    rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    published_by BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((published_by IS NULL) = (published_at IS NULL))
);

CREATE TABLE marketplace_price_versions (
    id BIGSERIAL PRIMARY KEY,
    version BIGINT NOT NULL UNIQUE CHECK (version > 0),
    rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    published_by BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((published_by IS NULL) = (published_at IS NULL))
);

CREATE FUNCTION marketplace_reject_published_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.published_at IS NOT NULL THEN
        RAISE EXCEPTION 'published marketplace versions are immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER marketplace_policy_immutable BEFORE UPDATE OR DELETE ON marketplace_policies
    FOR EACH ROW EXECUTE FUNCTION marketplace_reject_published_mutation();
CREATE TRIGGER marketplace_price_version_immutable BEFORE UPDATE OR DELETE ON marketplace_price_versions
    FOR EACH ROW EXECUTE FUNCTION marketplace_reject_published_mutation();

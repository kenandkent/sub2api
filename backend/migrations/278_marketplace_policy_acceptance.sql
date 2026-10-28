CREATE TABLE sharing_offer_policy_acceptances (
    id BIGSERIAL PRIMARY KEY,
    offer_id BIGINT NOT NULL REFERENCES sharing_offers(id) ON DELETE RESTRICT,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    offer_version BIGINT NOT NULL,
    policy_version BIGINT NOT NULL REFERENCES marketplace_policies(version) ON DELETE RESTRICT,
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER sharing_offer_policy_acceptances_immutable BEFORE UPDATE OR DELETE ON sharing_offer_policy_acceptances
    FOR EACH ROW EXECUTE FUNCTION supplier_review_audits_immutable();

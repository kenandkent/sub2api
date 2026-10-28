ALTER TABLE supplier_review_audits ADD COLUMN reviewed_version BIGINT;
ALTER TABLE supplier_review_audits ADD COLUMN agreement_version BIGINT;
ALTER TABLE supplier_review_audits ADD COLUMN application_fields JSONB;
ALTER TABLE sharing_offer_review_audits ADD COLUMN identity_reviewed BOOLEAN;

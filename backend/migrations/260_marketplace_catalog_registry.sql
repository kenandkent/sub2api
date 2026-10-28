-- A previously shared group never silently falls back to self-owned billing
-- when a newer price book removes it. Missing prices remain a closed gate.
CREATE TABLE marketplace_group_registry (
 group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE RESTRICT,
 first_price_version BIGINT NOT NULL,
 registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

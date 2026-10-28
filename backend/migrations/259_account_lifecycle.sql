-- Stable fingerprints of high-entropy API keys prevent duplicate offers.
CREATE TABLE marketplace_credential_fingerprints (
 provider TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 account_id BIGINT NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE RESTRICT,
 PRIMARY KEY (provider, fingerprint)
);

CREATE TABLE supplier_probe_limits (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
 last_probe_at TIMESTAMPTZ NOT NULL
);

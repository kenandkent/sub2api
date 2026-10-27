CREATE TABLE opencode_free_attempts (
    request_id text PRIMARY KEY,
    lease_token text NOT NULL,
    api_key_id bigint NOT NULL,
    user_id bigint NOT NULL,
    account_id bigint NOT NULL,
    profile_hash text NOT NULL,
    payload_hash text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending','ready','settled','unknown')),
    lease_until timestamptz NOT NULL,
    billing_source text,
    raw_usage jsonb,
    billing_command jsonb,
    usage_log jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX opencode_free_attempts_recovery_idx ON opencode_free_attempts (state, lease_until) WHERE state IN ('pending','ready');
CREATE TABLE opencode_free_sessions (
    key_hash text PRIMARY KEY,
    session_id text NOT NULL,
    expires_at timestamptz NOT NULL
);

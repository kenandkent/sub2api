-- A failing capture remains unsettled, but must not block unrelated suppliers.
CREATE TABLE marketplace_maturity_failures (
 capture_id BIGINT PRIMARY KEY REFERENCES marketplace_captures(id) ON DELETE RESTRICT,
 attempts BIGINT NOT NULL CHECK(attempts > 0),
 first_failed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_failed_at TIMESTAMPTZ NOT NULL,
 retry_after TIMESTAMPTZ NOT NULL
);

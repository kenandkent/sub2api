CREATE INDEX CONCURRENTLY IF NOT EXISTS accounts_owner_user_id_idx ON accounts(owner_user_id);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS accounts_id_owner_user_id_key ON accounts(id,owner_user_id);

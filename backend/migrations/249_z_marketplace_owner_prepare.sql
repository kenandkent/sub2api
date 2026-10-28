-- Add the nullable column without validating the whole accounts table while
-- holding an exclusive lock. Existing prototype databases already have both.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS owner_user_id BIGINT;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='accounts'::regclass AND conname='accounts_owner_user_id_fkey') THEN
  ALTER TABLE accounts ADD CONSTRAINT accounts_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT NOT VALID;
 END IF;
END $$;

-- 0002_option_poll_fk.sql — makes a vote's (poll_id, option_id) pair
-- structurally consistent: an option can only be voted on by the poll it
-- actually belongs to, enforced at the schema level via a composite
-- foreign key, not just the application's WHERE EXISTS guard.
--
-- Idempotent and re-run on every startup, like 0001 — there is no
-- migration-version tracking (see migrations.go), so every guard below
-- checks pg_constraint before acting. That makes this safe to run both
-- against a fresh database (created moments earlier by 0001, in the same
-- startup) and against one that predates this migration and already has
-- rows under the old, uglier options(id)-only foreign key.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'options_poll_id_id_key'
  ) THEN
    ALTER TABLE options ADD CONSTRAINT options_poll_id_id_key UNIQUE (poll_id, id);
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'votes_option_id_fkey'
  ) THEN
    ALTER TABLE votes DROP CONSTRAINT votes_option_id_fkey;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'votes_poll_id_option_id_fkey'
  ) THEN
    ALTER TABLE votes
      ADD CONSTRAINT votes_poll_id_option_id_fkey
      FOREIGN KEY (poll_id, option_id) REFERENCES options (poll_id, id) ON DELETE CASCADE;
  END IF;
END $$;

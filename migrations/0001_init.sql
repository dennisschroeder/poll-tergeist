-- 0001_init.sql — schema for poll-tergeist.
--
-- Applied at startup via embed.FS with CREATE TABLE IF NOT EXISTS, so it's
-- idempotent and needs no migration runner. See docs/adr for the trade-off.

CREATE TABLE IF NOT EXISTS polls (
  id         text PRIMARY KEY,                    -- 10-char base62, crypto/rand
  question   text NOT NULL CHECK (length(question) BETWEEN 1 AND 280),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS options (
  id       bigserial PRIMARY KEY,
  poll_id  text NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  position int  NOT NULL,                         -- 0..4, display order
  label    text NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
  UNIQUE (poll_id, position)
);

CREATE TABLE IF NOT EXISTS votes (
  id          bigserial PRIMARY KEY,
  poll_id     text   NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  option_id   bigint NOT NULL REFERENCES options(id) ON DELETE CASCADE,
  voter_token text   NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (poll_id, voter_token)
);

CREATE INDEX IF NOT EXISTS votes_poll_id_idx ON votes (poll_id);

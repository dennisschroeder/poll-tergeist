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
  UNIQUE (poll_id, position),
  UNIQUE (poll_id, id)                             -- lets votes FK against (poll_id, option_id)
);

CREATE TABLE IF NOT EXISTS votes (
  id          bigserial PRIMARY KEY,
  poll_id     text   NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  option_id   bigint NOT NULL,
  voter_token text   NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (poll_id, voter_token),
  -- Makes an option from a different poll impossible to insert, regardless
  -- of which application code performs the insert — not just an
  -- application-side WHERE EXISTS guard at insert time.
  FOREIGN KEY (poll_id, option_id) REFERENCES options (poll_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS votes_poll_id_idx ON votes (poll_id);

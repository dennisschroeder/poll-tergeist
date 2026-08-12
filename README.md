# poll-tergeist

A live voting/poll app: create a poll, share the link, watch results update in real time as votes
come in. One Go binary, one Postgres database, no frontend build step.

## Quick start

```bash
docker compose up -d
```

Then open `http://localhost:8080/`. That's the whole setup — no Go toolchain, no `npm install`,
nothing else to install. `docker compose` builds the server image from the repo's `Dockerfile`
(multi-stage: compiled in a `golang:1.26-alpine` stage, run from a minimal `alpine:3.20` image)
and starts it alongside Postgres, in dependency order — the app container waits for Postgres's
healthcheck before it starts.

Docker builds natively for whatever machine it's running on — Apple Silicon, Intel, or a Linux
x86/ARM host all get a correctly-arched image from the same `Dockerfile`, no cross-compilation
flags needed for local use. Windows works the same way through Docker Desktop, which always runs
Linux containers regardless of host CPU. The `TARGETOS`/`TARGETARCH` build args in the `Dockerfile`
also make this resolvable as a genuine multi-platform image via `docker buildx build --platform
linux/amd64,linux/arm64`, if it's ever published to a registry — not needed for local use, since
each machine already builds its own native image.

If port `8080` is already taken locally, override it without editing anything:
`APP_PORT=8091 docker compose up -d`. The Postgres port is fixed at `5432` — the test suite
(below) expects to find it there.

No separate build or install step for the frontend — the three HTML pages and their shared
`app.js` are embedded into the binary and served directly.

### Local development

Iterating on the Go code without rebuilding the image each time still needs the Go toolchain:

```bash
docker compose up -d db
go run ./cmd/server
```

This runs the server on the host, connecting to the same dockerized Postgres. Override with env
vars: `DATABASE_URL` (a full Postgres connection string) and `ADDR` (e.g. `:8090` if `8080` is
taken locally by something else).

## Domain glossary

- **Poll** — a question plus 2–5 Options. Identified by a short random ID; that ID *is* the
  shareable link (e.g. `/p/bXYOUUSd2L`).
- **Option** — one answer choice, ordered for display. Belongs to exactly one Poll.
- **Vote** — an immutable record that one Voter chose one Option in one Poll. Never updated,
  never deleted.
- **Voter token** — an opaque random string in an HttpOnly cookie. Not identity, not a user, not
  a session. Scopes "one vote per Poll" and nothing else.
- **Tally** — the derived count of Votes per Option. Never stored; always computed.

Deliberately *not* in the model: User, Account, Session, poll closing/expiry.

## API

| Method | Path | Notes |
|---|---|---|
| `POST` | `/api/polls` | `{question, options[]}` → `201` with the poll and a zero tally. Validates 2–5 options, non-blank labels, question ≤280 chars, labels ≤120 chars. |
| `GET` | `/api/polls/{id}` | Poll + options + current tally. `404` if unknown. |
| `POST` | `/api/polls/{id}/votes` | `{option_id}` → `201` with the new tally on a first vote, or `409` with the *current* tally on a repeat vote by the same voter. `400` if the option doesn't belong to this poll. |
| `GET` | `/api/polls/{id}/stream` | Server-Sent Events. Sends the current tally immediately, then one frame per subsequent vote on that poll. |

Views: `/` (create) → `/p/{id}` (vote) → `/p/{id}/results` (results). Voting redirects to
results whether the vote was fresh or a repeat.

## Data model

`migrations/0001_init.sql`, embedded and applied at startup (`CREATE TABLE IF NOT EXISTS`, so it's
idempotent — no separate migration runner or step).

```sql
create table polls (
  id         text primary key,          -- 10-char base62, crypto/rand
  question   text not null check (length(question) between 1 and 280),
  created_at timestamptz not null default now()
);

create table options (
  id       bigserial primary key,
  poll_id  text not null references polls(id) on delete cascade,
  position int  not null,                -- 0..4, display order
  label    text not null check (length(label) between 1 and 120),
  unique (poll_id, position)
);

create table votes (
  id          bigserial primary key,
  poll_id     text   not null references polls(id) on delete cascade,
  option_id   bigint not null references options(id) on delete cascade,
  voter_token text   not null,
  created_at  timestamptz not null default now(),
  unique (poll_id, voter_token)
);
```

Two deliberate choices worth knowing going in:

- `poll_id` is denormalized onto `votes` even though it's reachable through `option_id`. It exists
  so `UNIQUE(poll_id, voter_token)` can be enforced without a join. The redundancy is guarded at
  insert time: the vote is only written if the option's `poll_id` matches the poll being voted on
  (`INSERT ... SELECT ... WHERE EXISTS (...)`, one round trip).
- "2–5 options" is enforced in the handler, not the schema — a table `CHECK` can't count sibling
  rows. A trigger or a `CHECK` on a denormalized option count would close this gap; skipped here as
  out of scope for the box.

Votes are append-only and counts are always derived via `GROUP BY` — see
[`docs/adr/0001-append-only-votes.md`](docs/adr/0001-append-only-votes.md) for the alternatives
that were considered and rejected.

## Architecture

```
cmd/server/main.go        wiring, config from env, graceful shutdown
internal/poll/             domain: Poll, Option, Vote, Tally; ID gen; validation (no I/O)
internal/store/             pgx repository, hand-written SQL
internal/http/               handlers, routing, voter-cookie middleware
internal/live/                 SSE hub
migrations/, web/         embedded via embed.FS → one binary, no external assets
```

Live updates go through an in-process hub (`internal/live`): each poll has a set of subscriber
channels, and a vote publishes the new tally to all of them. Publish is a **non-blocking send**
(`select { case ch <- msg: default: }`) — a slow reader gets dropped frames instead of stalling the
voter whose request triggered the publish. That's the backpressure story.

The hub is per-process, which is the honest limitation: it doesn't fan out across a second
instance. See [`docs/adr/0003-tally-fan-out-and-queue-design.md`](docs/adr/0003-tally-fan-out-and-queue-design.md)
for why that's the right trade-off here, and what would replace it (Postgres `LISTEN/NOTIFY`) if
this had to run behind more than one process. The choice of SSE as the transport itself is a
separate decision — see
[`docs/adr/0002-sse-transport.md`](docs/adr/0002-sse-transport.md).

## Trade-offs and known gaps

- **Vote finality.** The first vote per (poll, voter) wins; the `UNIQUE` constraint is the entire
  enforcement, so there's no read-modify-write and no race to reason about. The cost is UX — no
  changing your vote. See [`docs/adr/0001-append-only-votes.md`](docs/adr/0001-append-only-votes.md).
- **Dedupe is a cookie, not identity.** `voter_token` stops accidental double-votes and double
  clicks, not a determined attacker clearing cookies or opening an incognito window. That's a
  deliberate, disclosed limitation, not an oversight.
- **Single-instance live updates.** The SSE hub lives in one process's memory. Horizontal scaling
  needs a shared fan-out layer (Postgres `LISTEN/NOTIFY` is the next step, Redis pub/sub beyond
  that) — not implemented, named explicitly in ADR 0003.
- **No deploy.** `docker compose up` is the full runnable answer here; no hosting account or CLI
  was in the critical path to a submission deadline. See
  [`docs/adr/0004-go-stdlib-no-build-frontend.md`](docs/adr/0004-go-stdlib-no-build-frontend.md)
  for the related frontend-stack reasoning.
- **No poll closing/expiry, no results-only mode, no edit-after-create.** Out of scope for the
  domain as modeled; see the glossary above.

## Tests

```bash
gofmt -l .
go vet ./...
TEST_DATABASE_URL="postgres://poll:poll@localhost:5432/poll_tergeist?sslmode=disable" go test ./... -race
```

`TEST_DATABASE_URL` gates every DB-backed test (`t.Skip` when unset) — `go test ./...` alone runs
the pure-domain tests only. The concurrency tests are the ones that matter for the "how is
concurrency handled" conversation:

- `TestConcurrentDistinctVoters` — 200 goroutines, distinct voter tokens, one poll. Asserts the
  tally sums to exactly 200: no lost votes, no contention between unrelated voters.
- `TestConcurrentSameVoter` — 50 goroutines, one shared voter token. Asserts exactly one insert
  succeeds and 49 come back as `ErrAlreadyVoted` — the `UNIQUE` constraint holding under a real
  race, not just in isolation.

Table-driven handler tests cover option-count bounds, blank/oversized input, an unknown poll ID
(`404`), and an `option_id` that belongs to a different poll (`400`).

Run with `-race`; it's the point, not an afterthought.

## AI usage

This project was built with AI assistance (Claude, via Claude Code) from a hand-written plan that
fixed every architectural decision — stack, schema, API surface, ADR structure — before any code
was generated. AI wrote the bulk of the Go and the three frontend pages against that frozen plan;
a human reviewed the diff, fixed the parts described below, and drove every ADR through explicit
review rather than accepting a first draft.

What AI generated largely as specified: the domain package, the Postgres repository layer, the SSE
hub, HTTP handlers and routing, the three static views, and the test suite structure (concurrency
tests plus table-driven handler tests).

What got corrected during review: an `embed` path mistake (a `//go:embed all:web` directive in
`cmd/server` that can't reach a sibling directory — fixed by giving `web/` its own small package
with its own embed directive, the same pattern already used for `migrations/`), and a test
ordering bug where a table truncation ran before migrations had created the tables it was trying
to truncate.

What was deliberately left out, and why it's named rather than hidden: horizontal scaling for live
updates, vote editing, a JS/CSS build pipeline, and deployment. Each has a stated trigger for when
it would become worth doing, in the relevant ADR or in the gaps list above — cutting them was a
scope decision under a time box, not an oversight.

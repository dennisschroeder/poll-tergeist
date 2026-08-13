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
| `POST` | `/api/polls/{id}/votes` | `{option_id}` → `201` on a first vote, `409` on a repeat vote by the same voter. `400` if the option doesn't belong to this poll. Acknowledges the mutation only — no tally in the body; read current state via `GET /api/polls/{id}` or the SSE stream. |
| `GET` | `/api/polls/{id}/stream` | Server-Sent Events. Sends the current tally immediately, then a refreshed tally after subsequent votes — internally driven by invalidation, so a burst of votes can coalesce into fewer frames than votes; the client always converges to the latest tally regardless. |

Views: `/` (create) → `/p/{id}` (vote) → `/p/{id}/results` (results). Voting redirects to
results whether the vote was fresh or a repeat.

## Data model

`migrations/0001_init.sql` and `migrations/0002_option_poll_fk.sql`, embedded and applied at
startup in filename order. There's no migration-version tracking or separate runner — every file
just needs to be idempotent and safe to re-run on every startup, which is why `0001` uses `CREATE
TABLE IF NOT EXISTS` and `0002` (added after `0001` originally shipped without the composite
foreign key below) guards each `ALTER TABLE` with a `pg_constraint` existence check. That makes
`0002` apply cleanly both to a brand-new database and to one created by an earlier version of
`0001` that's still running against a persistent `docker compose` volume — a plain `CREATE TABLE IF
NOT EXISTS` change to `0001` alone would silently no-op against any database that already existed,
leaving the invariant below undocumented-but-missing rather than actually enforced.

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
  unique (poll_id, position),
  unique (poll_id, id)                   -- lets votes FK against (poll_id, option_id)
);

create table votes (
  id          bigserial primary key,
  poll_id     text   not null references polls(id) on delete cascade,
  option_id   bigint not null,
  voter_token text   not null,
  created_at  timestamptz not null default now(),
  unique (poll_id, voter_token),
  foreign key (poll_id, option_id) references options (poll_id, id) on delete cascade
);
```

Two deliberate choices worth knowing going in:

- `poll_id` is denormalized onto `votes` even though it's reachable through `option_id`. It exists
  so `UNIQUE(poll_id, voter_token)` can be enforced without a join, and so the composite foreign key
  `(poll_id, option_id) REFERENCES options (poll_id, id)` can make an inconsistent pair — an
  `option_id` from a different poll than `poll_id` claims — impossible at the schema level,
  regardless of which application code performs the insert. The insert path also keeps an
  application-side `WHERE EXISTS` guard for a clean `ErrOptionNotFound`/`400` in the same round
  trip, rather than surfacing a raw foreign-key-violation error to the caller — but the schema, not
  that guard, is what actually guarantees the invariant.
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
internal/live/                 invalidation hub (signals only, no tally payloads)
migrations/, web/         embedded via embed.FS → one binary, no external assets
```

Postgres stores business truth synchronously — a vote is durably committed before `POST
/api/polls/{id}/votes` returns success (see
[`docs/adr/0005-synchronous-vote-persistence.md`](docs/adr/0005-synchronous-vote-persistence.md)).
The real-time layer never transports that truth; it only tells connected clients their view may be
stale:

```
Browser
   │
   │ POST vote
   ▼
Application
   │
   ▼
PostgreSQL
   │
   │ authoritative persisted vote
   ▼
invalidation
   │
   ▼
local Hub
   │
   ▼
SSE connection
   │
   │ get latest tally from PostgreSQL
   ▼
Browser
```

Concretely: after `InsertVote` commits, the handler calls `hub.Invalidate(pollID)` — not "publish
this tally." The hub (`internal/live`) tracks, per poll, a set of subscriber channels of type `chan
struct{}` with a buffer of exactly 1. `Invalidate` is a **non-blocking send**
(`select { case ch <- struct{}{}: default: }`): a subscriber already marked dirty stays dirty, and
a slow reader never stalls the voter whose request triggered the invalidation. Multiple votes in a
row collapse into one pending signal — that's intentional coalescing, not dropped data, because a
signal carries no tally of its own to lose.

The SSE handler (`internal/http/stream.go`) subscribes *before* reading anything from Postgres, then
sends the current tally, then loops: on every invalidation, it re-reads the tally from Postgres and
sends that. This ordering closes a real race — subscribing after the initial read could miss a vote
that lands in between, with no way to recover if no later vote ever arrives. Subscribing first means
that window doesn't exist; worst case is one redundant refresh right after connecting.

**Votes are durable. Invalidations are not — and that's intentional.** Losing or coalescing an
invalidation can never mean losing a vote, because the hub was never the place votes lived; it only
ever carried "go check again." Intermediate tally states don't need replay: a client that missed
five invalidations and gets the sixth reads the one thing it actually needs — the current state —
directly from the authoritative store. The design optimizes for every connected client eventually
converging on the latest committed tally, without depending on another vote occurring to trigger
that convergence.

The hub is per-process, which is the honest limitation: it doesn't fan out across a second
instance. See
[`docs/adr/0003-tally-fan-out-and-queue-design.md`](docs/adr/0003-tally-fan-out-and-queue-design.md)
for the invalidation-vs-snapshot-queue reasoning in full, and what would replace the per-process hub
(Postgres `LISTEN/NOTIFY`, as a first step) if this had to run behind more than one instance. The
choice of SSE as the transport itself is a separate decision — see
[`docs/adr/0002-sse-transport.md`](docs/adr/0002-sse-transport.md).

## Trade-offs and known gaps

- **Vote finality.** The first vote per (poll, voter) wins; the `UNIQUE` constraint is the entire
  enforcement, so there's no read-modify-write and no race to reason about. The cost is UX — no
  changing your vote. See [`docs/adr/0001-append-only-votes.md`](docs/adr/0001-append-only-votes.md).
- **Dedupe is a cookie, not identity.** `voter_token` stops accidental double-votes and double
  clicks, not a determined attacker clearing cookies or opening an incognito window. That's a
  deliberate, disclosed limitation, not an oversight — see "Voter identity" below for what it is,
  what it isn't, and how it could evolve.
- **Votes are synchronous, with no incoming queue.** `POST /vote` inserts and commits before it
  responds — "accepted" means "durably persisted," not "queued." See
  [`docs/adr/0005-synchronous-vote-persistence.md`](docs/adr/0005-synchronous-vote-persistence.md)
  for why, and what traffic pattern would justify revisiting it.
- **Single-instance live updates.** The invalidation hub lives in one process's memory. Horizontal
  scaling needs a shared fan-out layer (Postgres `LISTEN/NOTIFY` is the next step, Redis pub/sub
  beyond that) — not implemented, named explicitly in
  [`docs/adr/0003-tally-fan-out-and-queue-design.md`](docs/adr/0003-tally-fan-out-and-queue-design.md).
- **No deploy.** `docker compose up` is the full runnable answer here; no hosting account or CLI
  was in the critical path to a submission deadline. See
  [`docs/adr/0004-go-stdlib-no-build-frontend.md`](docs/adr/0004-go-stdlib-no-build-frontend.md)
  for the related frontend-stack reasoning.
- **No poll closing/expiry, no results-only mode, no edit-after-create.** Out of scope for the
  domain as modeled; see the glossary above.

## Production evolution

Three separate problems get called "scaling" or "add a queue," and this prototype deliberately
keeps them apart rather than reaching for one piece of infrastructure to cover all of them:

```
multiple app instances
→ horizontal live fan-out: Postgres LISTEN/NOTIFY may be sufficient for invalidation

burst vote ingestion beyond DB capacity
→ incoming load leveling: consider a durable incoming command queue

durable/replayable downstream events
→ durable event processing: consider a transactional outbox + broker
```

None of these is implemented today, and none is assumed to be automatically necessary — each has an
explicit, measurable trigger, documented in the relevant ADR, not a generic "production needs X"
justification:

1. **Horizontal live fan-out.** Triggered by actually needing a second instance (load or
   availability). Postgres `LISTEN/NOTIFY` is the natural first step — it keeps Postgres
   authoritative and only ever carries "state changed," the same invalidation semantics this
   prototype already uses within one process. Redis Pub/Sub is worth considering if
   `LISTEN/NOTIFY` itself becomes the bottleneck under measurement: notification throughput or DB
   signaling load, per-instance connection/topology constraints, or an operational requirement to
   decouple fan-out from Postgres entirely. See
   [`docs/adr/0003-tally-fan-out-and-queue-design.md`](docs/adr/0003-tally-fan-out-and-queue-design.md).
2. **Incoming load leveling.** Triggered by measured burst traffic exceeding sustainable synchronous
   write capacity, or a downstream outage that shouldn't mean rejecting votes. A database-backed
   work queue is a reasonable intermediate step before a dedicated broker. See
   [`docs/adr/0005-synchronous-vote-persistence.md`](docs/adr/0005-synchronous-vote-persistence.md).
3. **Durable event processing.** Triggered by a real downstream consumer needing individual vote
   *events* — durable delivery, replay, analytics — not just "the tally changed." A transactional
   outbox plus a broker (Kafka, RabbitMQ, NATS JetStream, …) is the shape that answers this; nothing
   today needs it. Note that plain NATS and Redis Pub/Sub are both at-most-once/ephemeral by
   default — durability specifically requires JetStream (for NATS) or an equivalent durable broker,
   not "a message broker" generically.

Not every production deployment of an app like this needs Kafka, Redis, or a distributed queue by
default — each is justified by a specific, measured requirement from the list above, not by the
mere fact of being "production."

## Voter identity

The `voter_token` cookie is **anonymous browser-level deduplication** — it exists to answer "has
this browser already voted in this poll," nothing else. It is explicitly **not** authentication, not
a verified identity, and not election-grade voter verification. See
[`docs/adr/0001-append-only-votes.md`](docs/adr/0001-append-only-votes.md) for why this is the
system's only identity concept at all.

One privacy property worth naming: the same raw token is currently reused across every poll a
browser votes in. That means votes from the same browser could, in principle, be correlated across
different polls by anyone with access to the `votes` table — not a capability this app exposes
today, but a property of the current schema worth knowing before extending it. A production
evolution would derive a poll-scoped identifier instead of reusing the raw token, e.g. conceptually
`HMAC(secret or voterToken, pollID)`, or move to authenticated user identity depending on product
requirements. Not implemented here — this prototype's dedupe need doesn't justify it.

The right level of identity depends entirely on what the poll is for:

```
casual anonymous poll
→ browser token

trusted internal poll
→ authenticated user identity

high-integrity voting
→ explicit eligibility/identity verification
```

This app targets the first tier. Moving to the second or third is a product decision, not a bug fix
— over-engineering identity into a casual poll app would be solving a problem nobody asked for.

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
- `TestCreatePoll_IDCollisionKeepsTransactionUsable` — proves a colliding poll ID no longer aborts
  the create-poll transaction (`ON CONFLICT DO NOTHING`, not a caught statement error), by inserting
  the same statement twice in one transaction and requiring a further insert in that same
  transaction to still succeed.

Live-update tests target the invalidation model from
[`docs/adr/0003-tally-fan-out-and-queue-design.md`](docs/adr/0003-tally-fan-out-and-queue-design.md),
split across two levels:

- `internal/live` (`hub_test.go`, no database) — the hub's own contract in isolation:
  `TestInvalidate_CoalescesWhilePending` (many invalidations while unconsumed collapse to exactly
  one pending signal), `TestInvalidate_NeverBlocks` (publication never blocks on a stalled
  subscriber), `TestInvalidateImmediatelyAfterSubscribe` (no window for a miss right after
  subscribing), plus scoping and unsubscribe behavior.
- `internal/http` (`stream_test.go`, DB-backed) — the same properties end to end, through the real
  SSE handler and Postgres: `TestStream_VoteRacingSubscriptionIsNotPermanentlyMissed` races a vote
  against the connection-establishment window the old read-then-subscribe ordering used to lose, and
  `TestStream_ConvergesAfterVoteBurstWithoutFurtherVotes` fires 30 concurrent votes without draining
  the stream, stops voting entirely, and asserts the client still reaches the exact final tally on
  its own — the failure mode the previous bounded-snapshot-queue design couldn't recover from.

Table-driven handler tests cover option-count bounds, blank/oversized input (including non-ASCII
and emoji, since length limits count Unicode code points via `utf8.RuneCountInString`, not UTF-8
bytes), an unknown poll ID (`404`), and an `option_id` that belongs to a different poll (`400`).

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

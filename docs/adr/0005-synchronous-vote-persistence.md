# ADR 0005 — Synchronous vote persistence and deferred command queue

**Status:** Accepted · **Date:** 2026-08-13

## Context

A vote has to end up durably in Postgres — that's the one fact the rest of the system (tally reads,
live invalidation) depends on. The question this ADR answers: does `POST /vote` wait for that
persistence to complete before responding, or does it hand the vote off to something else and
respond early? This is distinct from how live updates fan out (see
[ADR 0003](0003-tally-fan-out-and-queue-design.md)) and from whether a materialized tally is worth
maintaining (see [ADR 0001](0001-append-only-votes.md)) — it's specifically about the contract
`POST /vote` makes with its caller.

## Options considered

### Option A — Synchronous: insert, then respond

```text
POST /vote
    ↓
application
    ↓
PostgreSQL INSERT
    ↓
COMMIT
    ↓
HTTP success
```

**Pro:**
- The semantic contract is simple and strong: **when the API reports that the vote was accepted,
  the vote is already durably persisted in the authoritative store.** No client-side polling for a
  final status, no pending state to reconcile.
- `UNIQUE(poll_id, voter_token)` does all the dedupe/finality work inside the same request — a
  `409` response already reflects the database's final answer, not a guess made ahead of it.
- Failure is immediate and visible to the caller: a rejected vote is rejected in the response that
  asked, not discovered later through a side channel.

**Con:**
- A burst of concurrent votes writes directly against Postgres's write capacity — nothing absorbs a
  spike ahead of the database.
- A slow or momentarily unavailable database is felt directly by the voter, as a slow or failed
  request.

### Option B — Asynchronous: queue before persistence

```text
POST /vote
    ↓
durable queue
    ↓
HTTP 202 Accepted
    ↓
consumer
    ↓
PostgreSQL
```

**Pro:**
- Decouples request intake from write throughput — a burst is absorbed by the queue instead of
  hitting Postgres directly.
- A temporary downstream outage doesn't have to mean rejecting incoming votes.

**Con:**
- Changes what "accepted" means: `202` only means "queued," not "counted." The contract Option A
  gives for free — accepted means persisted — this option gives up.
- Duplicate or invalid votes (the same voter twice, a bad `option_id`) are discovered downstream,
  after the client already received a success response. The client then needs a way to learn the
  real outcome, which doesn't exist today.
- Introduces a pending/accepted/rejected lifecycle the client has to poll or subscribe for — new
  surface area with no current consumer.
- Does not remove the need for `UNIQUE(poll_id, voter_token)` — the constraint still has to be
  checked at the point of insert; the queue only delays when that happens.
- Introduces retry and idempotency concerns (what happens if the consumer crashes mid-insert and
  redelivers the message?) that Option A doesn't have.
- Operationally heavier: a queue to run, monitor, and reason about, for a request path with no
  measured burst problem to solve today.

## Decision

Option A: votes are inserted and committed synchronously, in the same request that reports success.

Nothing about current traffic demonstrates a need to buffer incoming votes — there is no measured
burst that exceeds Postgres's synchronous write capacity, and no downstream outage to absorb.
Option B is a real answer to a real problem, just not one this prototype currently has; adopting it
now would trade a simple, strong acceptance contract for operational complexity with no load to
justify it.

## Consequences

Makes easy: reasoning about what "the API said yes" means — it means the vote is in Postgres, full
stop. No lifecycle states, no reconciliation step, no separate status-check endpoint.

Makes hard: absorbing a traffic burst larger than Postgres can accept synchronously — today, that
shows up as slower or failed requests rather than queued ones.

**Revisit when:** measured burst traffic exceeds the sustainable synchronous vote-write capacity,
or temporary downstream outages need to be absorbed without rejecting incoming votes. A
database-backed work queue (e.g. a `pending_votes` table polled by a worker) is a reasonable
intermediate step before reaching for a dedicated broker — it reuses infrastructure already in
place. A dedicated broker is the step after that, if the database-backed queue itself becomes the
bottleneck. Do not implement the incoming queue now.

## Three different "queue" problems — not to be conflated

This project has, or could have, three distinct things that might get called "a queue." They solve
different problems, and none substitutes for another:

1. **Incoming command queue** — `browser → queue → vote persistence`. Purpose: load leveling /
   asynchronous command processing on the way *in*. This ADR's Option B, deliberately not built.
2. **Transactional outbox** — a vote and an outbox event written atomically in the same
   transaction. Purpose: reliably handing a committed database fact to a downstream messaging
   system, once one exists. Not built — there is no downstream consumer today.
3. **Live invalidation/fan-out** — `poll changed → connected clients refresh`. Purpose: UI
   freshness. Does not require durable per-event messaging — see
   [ADR 0003](0003-tally-fan-out-and-queue-design.md), which makes this exact distinction for the
   live-update path.

None of the three is implemented as a queue/broker today: votes are synchronous (this ADR); live
updates are invalidation signals, not an event log (ADR 0003); and there is no outbox because there
is no downstream system yet to hand events to.

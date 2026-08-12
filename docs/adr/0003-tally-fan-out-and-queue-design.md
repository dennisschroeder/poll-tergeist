# ADR 0003 — Tally fan-out and queue design

**Status:** Accepted · **Date:** 2026-08-12

## Context

Once a vote changes a tally, it has to reach every open connection watching that poll — including
connections on a different process, if there's ever more than one instance. That's two nested
questions, not one: how a published tally is queued and delivered to a single, possibly-slow
subscriber, and how a published tally reaches subscribers connected to a different instance
entirely. They're separate concerns — a poll app can pick any transport (see
[ADR 0002](0002-sse-transport.md)) with any answer to either question below — but they're argued
together here because the production-grade answer to the second question also answers the first.

## Options considered

### Option A — In-process hub, bounded per-subscriber channel
**Pro:**
- Zero external dependencies — the whole mechanism is a per-poll `map[chan []byte]struct{}` and a
  `make(chan []byte, 4)` per subscriber.
- `Publish` never blocks: a non-blocking send (`select`/`default`) means a slow reader gets dropped
  frames, not a stalled voter.

**Con:**
- Per-process only — a subscriber connected to instance A never sees a vote published on instance
  B.
- No delivery guarantee beyond the buffer: a subscriber more than 4 frames behind misses updates
  until it drains, and there's no replay for a reconnecting subscriber.

### Option B — Postgres `LISTEN/NOTIFY` for cross-instance fan-out
**Pro:**
- Closes the multi-instance gap with infrastructure already in use — no new service, just a
  `LISTEN poll_tally` per instance and a `NOTIFY` after each vote.

**Con:**
- Payload-size ceiling on `NOTIFY` (~8000 bytes) — fine for a tally, but a real constraint.
- One dedicated listening connection held per instance for the app's lifetime.
- Doesn't touch per-subscriber delivery — instances still need Option A's (or an unbounded, or a
  broker's) buffering underneath it for their own local subscribers.

### Option C — Redis pub/sub for cross-instance fan-out
**Pro:**
- Scales past `LISTEN/NOTIFY`'s limits; decouples fan-out from the database entirely.

**Con:**
- A new component to run, operate, and reason about.
- Still fire-and-forget (at-most-once) — doesn't add a delivery guarantee Option A's buffer
  doesn't already have; it only widens fan-out, not durability.

### Option D — External message broker (NATS or Kafka) for both concerns at once
**Pro:**
- The production answer: durable, replayable, at-least-once delivery, and consumer-group
  backpressure, handled by infrastructure built for exactly this — one piece of infrastructure
  resolving both cross-instance fan-out and per-subscriber delivery together.

**Con:**
- A new service to run, operate, and reason about, for a live-update codepath that's currently
  about 40 lines end to end.
- Buys guarantees with no consumer here: a missed SSE frame is a few hundred milliseconds of UI
  staleness until the next vote refreshes it — not a lost fact, since the vote itself is already
  durable in Postgres ([ADR 0001](0001-append-only-votes.md)). Durability and replay for a value
  that resends itself in full on every change is insurance against a risk that doesn't exist yet.

## Decision

Option A: an in-process hub with a bounded (size 4), drop-on-full channel per subscriber.

Each published frame is a full tally snapshot, not a delta, so dropping one is self-healing — the
next vote anywhere on the poll re-sends the complete current state, and a client that missed two
frames is fully correct again the moment the next one arrives. That property is what makes "drop
and don't block" safe here in a way it wouldn't be for a delta or event-sourced stream, and it's
also why Option D's durability buys nothing yet: nothing is lost that isn't already recoverable by
construction. An unbounded channel was considered and rejected outright — it trades a bounded,
self-healing staleness for an unbounded memory leak against a single stalled client, for no
compensating benefit.

The per-process limitation is accepted for the same reason the queue design is: nothing in this
app's current scope runs more than one instance. Options B and C are both real, named answers to
that specific gap, deliberately not built.

## Consequences

Makes easy: the entire fan-out and buffering mechanism stays about 40 lines and is readable in one
sitting — concurrency, backpressure, and the multi-instance gap are all visible in the same small
file. Makes hard: running more than one server instance (votes on one instance are invisible to
viewers connected to another), and recovering from more than 4 missed frames without waiting for
the next vote.

Two independent triggers to revisit, not one:

- **A second instance is genuinely needed** (load or availability). `LISTEN/NOTIFY` (Option B) is
  the first step; Redis pub/sub (Option C) is the step after that, when `LISTEN/NOTIFY`'s payload
  limits or per-instance connection cost themselves become the bottleneck.
- **Tallies stop being idempotent full snapshots** — per-vote delta events, or an audit/analytics
  consumer that needs replay. That's the point where the self-healing argument for Option A no
  longer holds, and a broker (Option D) stops being unused insurance and starts being the correct
  answer — likely resolving the multi-instance trigger above in the same move.

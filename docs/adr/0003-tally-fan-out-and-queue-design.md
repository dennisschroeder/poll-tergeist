# ADR 0003 — Tally fan-out: invalidation signals, not snapshot queueing

**Status:** Accepted · **Date:** 2026-08-12 (revised 2026-08-13)

## Context

Once a vote commits, every open connection watching that poll has to find out — including
connections on a different process, if there's ever more than one instance. That's two nested
questions, not one: how a change is delivered to a single, possibly-slow subscriber, and how a
change reaches subscribers connected to a different instance entirely. They're separate concerns —
a poll app can pick any transport (see [ADR 0002](0002-sse-transport.md)) with any answer to either
question below.

The central invariant, unchanged from [ADR 0001](0001-append-only-votes.md): **Postgres is
authoritative.** Connected clients do not need every intermediate tally — they need to eventually
converge on the latest committed tally. A client must not require another user to cast a vote in
order to recover from a missed or coalesced update: if voting stops, every connected client must
still reach the final state on its own.

### Why the original queued-snapshot design didn't hold

The first version of this hub kept a small buffered channel (size 4) of *serialized tally
snapshots* per subscriber, and dropped new messages once the channel was full. That's queue
semantics applied to a problem that isn't a queue: it treats each tally as an event worth keeping
in order, when only the newest one actually matters.

The failure mode:

```text
client buffer:
101, 102, 103, 104

new tallies:
105 ... 150

if new messages are dropped:
client eventually reaches 104
database is at 150
```

Once the buffer fills, `Publish` drops whichever frames don't fit — but a bounded channel drops
*new* arrivals, not old ones, so the client drains stale states (101-104) it no longer needs while
the one state it actually wants (150) was never queued at all. If voting then stops, the client has
no further trigger to catch up and stays wrong indefinitely. The old design's "self-healing" claim
only held as long as another vote kept arriving to resend the full state — which is exactly the
dependency this ADR now rules out.

The key observation: **older snapshots have almost no value once a newer one exists; the newest
state carries all the value.** A design that preserves old states while discarding the newest one
has the priority backwards.

## Options considered

### Option A — In-process hub, per-subscriber invalidation signal
**Pro:**
- Zero external dependencies — a per-poll `map[chan struct{}]struct{}` and a `make(chan struct{},
  1)` per subscriber.
- `Invalidate` never blocks: a non-blocking send (`select`/`default`) into a 1-slot channel either
  marks a subscriber dirty or leaves it dirty — there's nothing to drop, because there's no payload
  to lose.
- Coalescing is the design, not an accident: five votes in a row collapse to one pending signal,
  and the subscriber reacts by reading current state once, not by replaying five updates.
- Whatever a subscriber actually sends to its browser is read fresh from Postgres at the moment
  it's needed — never a value computed by, and possibly stale relative to, a concurrent request.

**Con:**
- Per-process only — a subscriber connected to instance A never sees an invalidation published on
  instance B.
- A reconnecting subscriber gets whatever Postgres currently holds, not a history of what changed
  while it was away — by design (see Consequences), but worth stating plainly.

### Option B — In-process hub, bounded per-subscriber channel of tally snapshots (previous design)
**Pro:**
- Self-healing *as long as voting continues*: each frame is a full snapshot, so a client that
  missed some frames is fully correct the moment the next one arrives.

**Con:**
- Wrong semantics for "converge to latest": a full buffer drops the newest state, not the oldest,
  so a burst of votes can leave a client stuck several votes behind.
- No recovery if voting stops — see the failure mode above. The client's correctness became
  contingent on continued vote traffic it has no control over.
- Carries a serialized payload per pending message for no benefit a `struct{}` doesn't already
  provide, since the payload is discarded and re-read from Postgres on delivery anyway once
  Option A's re-read-on-signal approach is available.

### Option C — Unbounded per-subscriber queue of snapshots
**Pro:**
- Never drops a message.

**Con:**
- Unbounded memory against a single stalled client — a memory leak with no compensating benefit,
  since intermediate states aren't needed anyway (see the central invariant above).

## Decision

Option A: the hub carries invalidation signals, not tally data. Its message changes from

```text
"The tally is A=12, B=8"
```

to

```text
"Poll X changed; your currently known tally may be stale."
```

That's an invalidation, not a value. A subscriber holding a pending signal doesn't need a second
one — both mean exactly the same thing ("go re-read Postgres"), so multiple invalidations coalesce
safely:

```text
vote
vote
vote
vote
vote
```

can become:

```text
DIRTY
```

because the consumer only needs to obtain the newest authoritative state. In code:

```go
select {
case ch <- struct{}{}:
    // subscriber is now marked dirty
default:
    // already dirty; nothing else required
}
```

This is intentional coalescing, not accidental message loss. The subscriber-side loop (the SSE
handler, see [ADR 0002](0002-sse-transport.md)) reacts to a signal by calling `store.GetTally`
again and sending whatever Postgres currently says — always the true current state, never a value
carried by the invalidation itself.

### Connection-order race

The original SSE handler read the tally, sent it, and only then subscribed:

```text
1. Read tally
2. Send initial tally
3. Subscribe to changes
```

A vote committing between steps 1 and 3 produces an invalidation nobody is listening for yet — and
if no *later* vote arrives, that missed vote is gone for good as far as this client is concerned.
The fix is ordering, not buffering:

```text
1. Subscribe
2. Read current tally
3. Send current tally
4. On invalidation, read current tally again
```

Subscribing first means any vote that commits during step 2 still produces a signal the loop will
see — worst case, one redundant refresh right after the initial send, which is acceptable.
Correctness here rests on eventual convergence, not on avoiding every possible redundant read.

## Consequences

Makes easy: reasoning about staleness — a subscriber is either caught up or has exactly one pending
"go check" signal, never a queue of stale values to reconcile. The mechanism stays about the same
size as the previous design while being correct under the failure mode that actually matters here:
voting stopping while a client is behind.

Makes hard: the same things as before — running more than one server instance (an invalidation
published on instance A is invisible to a subscriber on instance B), and there's still no
replay/history for a client that wants to know *what* changed, only *that* something did. That's
accepted: nothing this app does needs per-event replay on the live-update path (see
[ADR 0005](0005-synchronous-vote-persistence.md) for the related, and deliberately separate,
decision about incoming vote queueing).

### Multi-instance evolution

The likely first step, if a second instance is ever genuinely needed for load or availability:

```text
vote transaction
      ↓
PostgreSQL
      ↓
NOTIFY poll_changed
      ↓
all application instances
      ↓
local invalidation/fan-out
      ↓
SSE clients
```

Postgres `LISTEN/NOTIFY` is the natural first step, not a stopgap: Postgres is already
authoritative, and a `NOTIFY` payload only ever needs to mean "state changed" — the same
invalidation-not-data semantics this ADR already settled on, just carried across processes instead
of staying within one. No new service to run.

Running multiple instances does not, by itself, mean Redis, Kafka, or NATS becomes required. Each
of those solves a different problem, justified by a different requirement, not by instance count:

- **Postgres `LISTEN/NOTIFY`** — simple cross-instance invalidation, as long as Postgres remains
  authoritative and the message stays "something changed." The default next step.
- **Redis Pub/Sub** — worth considering if `LISTEN/NOTIFY`'s per-instance connection cost or
  payload ceiling (~8000 bytes) becomes the actual bottleneck under measurement, or if operational
  scale otherwise justifies it. Still fire-and-forget, at-most-once — it widens fan-out; it doesn't
  add a delivery guarantee `LISTEN/NOTIFY` lacks.
- **A durable broker** (Kafka, RabbitMQ, NATS JetStream, Google Pub/Sub, …) — justified when
  individual *events* (not just "something changed") need durable delivery, replay, independent
  consumers, stronger delivery semantics, analytics/event processing, or decoupling at a larger
  scale. That's a different problem than live UI invalidation; see
  [ADR 0005](0005-synchronous-vote-persistence.md) for the distinction between an incoming command
  queue, a transactional outbox, and live fan-out — three different "queue" problems this project
  deliberately does not conflate.

One naming precision worth stating explicitly: **core NATS is ephemeral and at-most-once** — the
same delivery guarantee as Redis Pub/Sub or this ADR's in-process channel; it is not durable by
itself. **JetStream** is the NATS feature that adds persistence and durable/replayable delivery.
Describing "NATS or Kafka" as a single generically-durable bucket conflates the two — whichever is
chosen, it should be chosen for a specific durability/replay requirement, not for the name.

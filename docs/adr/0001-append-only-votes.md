# ADR 0001 — Append-only votes, derived tallies

**Status:** Accepted · **Date:** 2026-08-12

## Context

Every vote has to be counted, and every poll needs a live tally. The question is where the count
lives: computed from individual vote rows, or maintained as a running number. This also has to
carry the app's only identity concept — the voter token — since "one vote per voter" is the one
integrity rule the system actually enforces. Load is low (a coding-challenge poll app, not a
production voting platform), so raw read throughput isn't the deciding factor; what a Lead
Engineer is expected to reason about here is auditability, correctness under concurrent writes, and
what each option costs to defend later.

## Options considered

### Option A — Append-only `votes` table, counts via `GROUP BY`
**Pro:**
- Every vote is a permanent, auditable row — a recount is just re-running the query.
- Writes never contend on a shared row: concurrent voters on the same option insert into an
  unbounded table, not compete for one lock.
- Dedupe and vote finality both fall out of a single `UNIQUE(poll_id, voter_token)` constraint.

**Con:**
- Reads recompute the tally from scratch every time (a scan + `GROUP BY`, not an index lookup).

### Option B — Counter column on `options`
**Pro:**
- Reads are a single indexed row fetch — the fastest possible tally.
- `UPDATE options SET votes = votes + 1` is atomic under Postgres row-level locking, so this is
  *not* a lost-update bug.

**Con:**
- Every vote on the same option serializes on that option's row lock — a popular option becomes a
  point of write contention as concurrent voters queue behind it.
- No audit trail: a wrong count can't be explained or recomputed.
- No natural place to enforce one-vote-per-voter without a second table anyway, at which point the
  counter is redundant bookkeeping alongside it.

### Option C — Both, kept in sync
**Pro:**
- A possible optimization once read throughput actually demands it — audit trail from the vote log,
  fast reads from the counter, both at once. Not "the production-grade answer" by default: the
  derived tally in Option A may remain fully valid in production until measurement says otherwise.

**Con:**
- Pays the complexity of Option A (still need the audit table, still need the dedupe constraint)
  *and* has to keep two representations of the same fact consistent under concurrent writes — the
  "overengineered half-finished" shape a scoped exercise should avoid.

## Decision

Option A: an append-only `votes` table with tallies always computed via `GROUP BY`, never stored.

It's the only option where dedupe, auditability, and finality all come from one constraint, with
zero extra bookkeeping. Option B's atomicity is real, but hot-row contention plus losing audit and
recount is a bad trade for what this needs. Option C buys both option's strengths but also both of
their costs, plus keeping them in sync — complexity with no consumer at this scale.

It's also the simplest option that satisfies the requirements — no counter to keep in sync, no
upsert logic, first vote wins and the `UNIQUE` constraint is the entire enforcement. That
simplicity comes with an accepted usability cost: a voter can't change their mind once cast.
Allowing upsert-on-conflict would let the row mutate, breaking the append-only invariant this
decision rests on — reopening that door costs exactly what Option C was rejected for.

## Consequences

Makes easy: recounting, auditing, and reasoning about correctness under concurrency — the whole
enforcement story is one constraint, not application logic. Makes hard: nothing at this app's
scale; reads stay a cheap indexed `GROUP BY` on `votes(poll_id)` well past any load this exercise
will see.

**Revisit when:** measured tally-read cost or latency actually requires it — not preemptively, and
not because a materialized/maintained tally is assumed to be "how production does it." The derived
tally may remain fully valid in production indefinitely; a counter column kept in sync via the same
transaction that inserts the vote (Option C) is a possible optimization to introduce once
measurement demands it, and even then it's an addition, not a replacement — the vote log stays the
source of truth. Do not implement Option C now.

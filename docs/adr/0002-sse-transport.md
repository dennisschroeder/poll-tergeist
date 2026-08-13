# ADR 0002 — SSE as the live-update transport

**Status:** Accepted · **Date:** 2026-08-12

## Context

This is a *live* poll app — results have to move without a page reload. The actual requirements the
transport has to satisfy: communication only ever flows server → browser (a viewer's tab never has
to send anything back over the same connection); updates are event-driven — a vote lands, or it
doesn't, there's no continuous stream of data; near-real-time delivery is the goal, not
sub-second/hard-real-time; and the client side should stay simple, ideally using what the browser
already provides rather than a hand-rolled reconnect/keepalive protocol.

This decision is just the wire protocol between server and browser: how a tally change gets from
the server to a viewer already looking at the results page. How that change is signaled and fanned
out — within one process and across more than one — is a separate, orthogonal decision; see
[ADR 0003](0003-tally-fan-out-and-queue-design.md).

## Options considered

### Option A — Server-Sent Events (SSE)
**Pro:**
- One-directional fits the actual traffic: server → browser tally pushes, nothing needs to flow
  back over the same connection.
- Plain HTTP — no protocol upgrade, no extra dependency; `net/http`'s `Flusher` is enough.
- Native browser reconnect via `EventSource`, for free.

**Con:**
- One connection per viewer held open server-side — a cost every push-based transport shares, not
  specific to SSE.

### Option B — Polling every 2s
**Pro:**
- Simplest possible implementation; genuinely defensible at this scale.

**Con:**
- Doesn't match "event-driven": a fixed interval is either too slow (a vote waits up to the full
  interval to show up) or wasteful (most polls return "nothing changed"), and there's no way to
  pick an interval that's both near-real-time and low-overhead at the same time.
- Every open results page generates load proportional to how long it's open, not to how many votes
  actually happen — the opposite of what an event-driven update should cost.

### Option C — WebSockets
**Pro:**
- Bidirectional, and the most recognizable "real-time" name.

**Con:**
- A duplex transport for a stream that only ever flows one way here — the client never sends
  anything over the socket, and the actual requirement (server → browser, event-driven) doesn't
  need the capability this buys.
- Extra dependency, plus upgrade handshake and ping/pong keepalive to implement and maintain, for
  capability this app doesn't use.

### Option D — No live updates
**Pro:**
- Saves the most time of any option.

**Con:**
- It's a live poll app; a manual refresh to see the count change is the first thing a reviewer
  would notice.

## Decision

Option A: Server-Sent Events, via `net/http`'s `Flusher` and the browser's native `EventSource`.

SSE is the simplest protocol that actually matches the communication pattern: one-directional,
event-driven, near-real-time, server → browser only. A duplex transport (Option C) buys capability
nobody uses here. Polling (Option B) can't express "push when it happens" — only "ask again later,"
which is either laggy or wasteful. Option D is the one true strawman here — a live poll app that
doesn't push live updates has already failed its own name.

## Consequences

Makes easy: live updates with no extra dependency and free reconnect-on-drop, on infrastructure
(`net/http`) already in use for everything else. Makes hard: nothing at the transport level itself
— the actual limitations (per-process fan-out, per-subscriber buffering) live one layer down, in
how published tallies reach subscriber connections, and are argued in
[ADR 0003](0003-tally-fan-out-and-queue-design.md).

Revisit if the traffic pattern stops being one-directional — e.g. the browser needs to push
something back over the same connection. Nothing in this app's current scope does that.

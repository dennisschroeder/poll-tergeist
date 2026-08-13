# ADR 0004 — Go stdlib backend, no-build HTML/JS frontend

**Status:** Accepted · **Date:** 2026-08-12

## Context

The brief asks for a backend API (REST or GraphQL) plus at least two views — one to vote, one for
results — built within a fixed, roughly bounded time box, and explicitly warns against an
overengineered, half-finished result. Within those constraints, the stack decision is really a
question of where the time box is best spent: on infrastructure and tooling, or on the actual
domain logic — validation, concurrency, correctness — that the rest of this project's ADRs argue
through in detail.

## Options considered

### Option A — Go stdlib `net/http` API, no-build HTML/JS frontend
**Pro:**
- Every hour spent goes toward domain logic — concurrency, SQL, HTTP — rather than tooling setup.
- The frontend (three views + shared `app.js`, embedded via `embed.FS`) stays under 500 lines
  total and needs no `npm install`, bundler, or build step to run or review.
- One binary, one `docker compose up` — nothing else to install to see it work.

**Con:**
- Doesn't demonstrate a modern component-framework frontend.

### Option B — Go API + React/Vite SPA
**Pro:**
- Broader stack coverage; React is the brief's own first example of a frontend option.

**Con:**
- Spends real time on bundler config, component structure, and client-side routing rather than
  domain logic, inside a fixed box.
- A thin two-page React app is a lot of setup for two views that don't need componentization.

### Option C — TypeScript full-stack (e.g. Next.js)
**Pro:**
- One language across the whole stack; fastest path to something that looks polished.

**Con:**
- Doubles the number of languages the domain logic has to be explained in for no functional gain,
  since the brief only requires one backend and two views.
- Trades a proven concurrency model (goroutines, channels, `-race`) for one that would need to be
  built from scratch in a different runtime.

### Option D — Go + templ/HTMX (server-rendered)
**Pro:**
- The hub itself carries no payload — it only ever signals "this poll changed" (see
  [ADR 0003](0003-tally-fan-out-and-queue-design.md)); the SSE handler is the sole place that reads
  Postgres and formats each frame. A htmx-flavored variant would only need to change what that one
  handler renders on invalidation — a rendered HTML fragment instead of a JSON tally — without
  touching the hub. No client-side JS would be needed to parse a tally and recompute bar widths by
  hand.
- One representation of "what a tally row looks like" instead of three (a Go struct, its JSON
  encoding, and hand-written JS rendering logic) kept in sync, as the current results page does.

**Con:**
- `POST /polls/{id}/votes` would return an HTML fragment scoped to one page's DOM target, not a
  resource representation — the same endpoint can't usefully serve both an HTMX browser and a
  plain `curl` request or a future non-HTML client with one response. Satisfying "backend API"
  unambiguously would mean a second, parallel JSON response path alongside it.
- templ's code-generation step only costs the developer, not a reviewer, if the generated `.go`
  files are committed — but it's still a toolchain dependency and a workflow step Option A has
  none of.

## Decision

Option A. It's the only one of the four that keeps every requirement from the brief satisfied
directly — a genuine backend API (unlike Option D) — while spending the time box on the domain
logic the rest of this project is built to demonstrate, rather than on frontend tooling (unlike
Options B and C). The frontend's simplicity is a stated, deliberate cut — three pages and a shared
script, no component framework — not a gap discovered after the fact. Option D is the closest
call of the three rejected options — its rendering model is genuinely coherent, not a worse
version of Option A — but it still costs a parallel JSON path to keep the API requirement
unambiguous and a toolchain Option A doesn't need at all, for a live-update convenience the
current SSE-plus-vanilla-JS pairing already provides.

## Consequences

Makes easy: reviewing the entire codebase, frontend included, in one sitting with no build step,
no `node_modules`, and no framework-specific context required. Makes hard: showing depth with a
modern component framework — genuinely not demonstrated here, and worth saying plainly rather than
implying otherwise.

**This is a prototype-scoped decision, not a permanent one.** It optimizes for a small
implementation footprint, minimal tooling, low setup cost, and spending the time box on
architecture rather than frontend framework work — exactly the trade-off a time-boxed prototype
should make.

**Revisit when a concrete production requirement calls for it** — not preemptively, and not
because a framework is assumed to be "how production does frontend." Candidate triggers: UI
complexity that outgrows hand-rolled DOM updates, a need for component reuse across more than a
handful of views, a team structured around dedicated frontend ownership, accessibility workflows a
framework's tooling supports better than raw HTML/JS, frontend test tooling, or design-system
integration. None of those exist yet. At that point the Option A/B trade-off needs re-arguing from
scratch against the actual requirement, not just re-weighted in the abstract — do not add a
frontend framework now.

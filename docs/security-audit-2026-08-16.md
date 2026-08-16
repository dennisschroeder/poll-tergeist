# Security audit — 2026-08-16

Point-in-time review of the whole codebase at commit `95db22e`. No code was changed. Findings are
recorded here only.

## Summary

The application has no injection vulnerabilities and no cross-site scripting surface. Every SQL
statement is parameterized, every static asset path is a compile-time constant, and all
user-controlled text reaches the DOM through `textContent`. The classic OWASP injection categories
are closed.

The exposure is concentrated in three areas instead:

1. **Resource exhaustion.** Neither request bodies nor request rates nor SSE subscriber counts are
   bounded anywhere. A single unauthenticated request can allocate arbitrary memory.
2. **Transport and browser hardening.** No security headers, and the voter cookie lacks `Secure`.
3. **Operational posture.** The application's database role owns its own schema and executes DDL on
   every startup, the compiled-in database default is unencrypted with known credentials, and
   nothing is logged.

Counted by severity: 2 high, 8 medium, 10 low. The two high findings are both availability issues on
unauthenticated endpoints.

A dependency scan (`govulncheck`, run 2026-08-16) reports five reachable known vulnerabilities — four
in the Go 1.26.5 standard library and one in `golang.org/x/text` v0.29.0. Both are one-line upgrades.

### Scope and threat model

The README frames this explicitly as a time-boxed prototype targeting "casual anonymous poll", and
several properties an auditor would otherwise flag are already disclosed there as deliberate
trade-offs — cookie-based dedupe is not identity, live fan-out is single-instance, there is no poll
closing or expiry. This audit does not re-litigate those. Where a finding overlaps a disclosed
trade-off, it says so and reports only the part that goes beyond what is documented.

Two deployment contexts are rated separately throughout, because they differ sharply:

- **Local prototype** — `docker compose up` on a developer machine, reachable only from localhost.
- **Public deployment** — the same binary exposed to the internet, which the README names as a
  possible next step ("No deploy" is listed as a known gap, not as a permanent decision).

Severity ratings below are for the public-deployment context. For the local prototype almost
everything drops to informational, since the attacker would already be on the host.

### Assets worth protecting

| Asset | Exposure |
|---|---|
| Poll integrity (tally reflects real votes) | Deliberately weak — dedupe is a cookie, disclosed |
| Poll availability | Weak — no rate limiting or body limits |
| Poll confidentiality | Capability URL — anyone with the 10-char ID can read and vote |
| Voter privacy (which browser voted for what) | Stored in plaintext, correlatable across polls |
| Database integrity | App role has full DDL rights |

### Verification status

Findings marked **verified** were confirmed by running code or a tool. Findings marked **static**
follow from reading the source and from documented platform behaviour, and were not reproduced
against a running instance — no Postgres or Docker daemon was available on the audit host. Nothing
here is speculative about what the code does; the distinction is about whether the runtime effect was
observed.

---

## High

### SEC-01 — Unbounded request body on both POST endpoints

**Severity:** High (public) / Informational (local) — **Static**
**Location:** [`internal/http/handlers.go:75`](../internal/http/handlers.go), [`internal/http/handlers.go:144`](../internal/http/handlers.go)

Both handlers decode directly from `r.Body` with no `http.MaxBytesReader` and no `Content-Length`
check:

```go
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
```

`json.Decoder` reads a value to completion before returning, so the size of the allocation is chosen
by the client. A `POST /api/polls` carrying a single 2 GB `question` string allocates 2 GB before
`poll.ValidateCreate` ever runs — validation happens at [`handlers.go:79`](../internal/http/handlers.go),
strictly after the decode. The same applies to the `options` array: `MaxOptions = 5` is enforced
after the entire array is materialized, so ten million option strings are decoded first and rejected
second.

`docker-compose.yml` sets no memory limit on the `app` service, so the ceiling is host RAM. One
request, no authentication, no rate limit in front of it.

**Fix sketch:** wrap the body in `http.MaxBytesReader(w, r.Body, 64<<10)` in both handlers, or once in
a middleware for all `POST` routes. The valid maximum payload is bounded by `MaxQuestionLen` (280)
plus five times `MaxLabelLen` (120), so a limit in the low tens of kilobytes is generous. Return 413
when the limit trips.

### SEC-02 — No rate limiting on any endpoint

**Severity:** High (public) / Informational (local) — **Static**
**Location:** [`internal/http/router.go:19`](../internal/http/router.go) — all four routes

There is no rate limiting, quota, proof-of-work, or CAPTCHA anywhere in the request path. The
`voterCookie` middleware is the only middleware. Consequences, each independently exploitable without
authentication:

- **Unbounded poll creation.** `POST /api/polls` inserts one `polls` row plus up to five `options`
  rows per call, with no cap on polls per client, per IP, or globally. Disk fills; nothing reclaims
  it, because there is no expiry or deletion path (the README names the absence of expiry as a scope
  decision, but its effect on abuse is not covered there).
- **Unbounded vote insertion.** The `UNIQUE (poll_id, voter_token)` constraint stops a *repeat* vote
  for a given token, and a client that discards its cookie gets a new token per request. Vote rows
  per poll are therefore unbounded, and each one triggers a fan-out (see SEC-04).
- **Unbounded SSE connections.** Each `GET /api/polls/{id}/stream` holds a goroutine, a TCP
  connection, and a hub subscription for as long as the client keeps it open.

The README discloses that cookie dedupe does not stop a determined attacker. It does not discuss
volumetric abuse, which is a distinct problem: the concern here is the cost of the requests, not the
truthfulness of the tally.

**Fix sketch:** per-IP token buckets at the edge (reverse proxy) or in middleware, with separate,
much tighter limits for `POST /api/polls` than for reads. A cap on concurrent SSE connections per IP.
Longer term, poll expiry gives the storage growth a ceiling.

---

## Medium

### SEC-03 — Missing server timeouts

**Severity:** Medium — **Static**
**Location:** [`cmd/server/main.go:43`](../cmd/server/main.go)

```go
srv := &http.Server{
    Addr:              addr,
    Handler:           handler,
    ReadHeaderTimeout: 5 * time.Second,
}
```

`ReadHeaderTimeout` closes the classic Slowloris header attack. Three gaps remain:

- **No `ReadTimeout`.** A client that sends valid headers and then dribbles the body one byte per
  second holds a goroutine and a connection indefinitely. Combined with SEC-01, a slow 1 GB body is
  both a memory and a connection cost.
- **No `IdleTimeout`.** Keep-alive connections are never reaped. Go falls back to `ReadTimeout` for
  idle connections, which is also unset, so idle connections persist until the client leaves.
- **No `WriteTimeout`.** This one is deliberate-by-necessity rather than an oversight — a
  `WriteTimeout` would kill long-lived SSE streams, since it covers the whole response. The correct
  form is a `ReadTimeout` plus per-handler write deadlines via `http.ResponseController` on the
  non-SSE routes, which is more work than adding a field.

Note that `GO-2026-6089` (SEC-15) is precisely a bug in `ReadHeaderTimeout` enforcement for the
unencrypted HTTP/2 path, so the one timeout that is set is partially ineffective on the current Go
version.

**Fix sketch:** set `ReadTimeout` and `IdleTimeout` on the server; leave `WriteTimeout` unset and add
`http.ResponseController.SetWriteDeadline` in the JSON handlers, with the SSE handler exempt.

### SEC-04 — SSE fan-out amplification and slow-reader goroutine pinning

**Severity:** Medium — **Static**
**Location:** [`internal/http/stream.go:50`](../internal/http/stream.go), [`internal/live/hub.go:49`](../internal/live/hub.go)

Two distinct problems in the live path.

**Amplification.** `Hub.Invalidate` signals every subscriber for a poll, and each subscriber
independently issues its own `GetTally` query. One vote on a poll with N open streams produces N
Postgres aggregate queries. The coalescing in `Invalidate` (documented in ADR 0003) bounds *queued*
signals per subscriber to one, and does not bound the query rate — a steady vote stream keeps every
subscriber querying. With no cap on subscribers (SEC-02) and a pgx pool defaulting to
`4 × NumCPU` connections, a modest number of streams on one popular poll saturates the pool and
degrades every other request. The write-side cost of a vote is one INSERT; the read-side cost is N
aggregations. That ratio is the attack.

**Goroutine pinning.** The stream loop selects on `ctx.Done()` and the change channel, then calls
`w.Write` inside `writeSSEFrame`. A client that opens a stream and stops reading fills the socket
buffer, at which point `w.Write` blocks. `ctx.Done()` cannot be observed while blocked inside
`Write`, and there is no write deadline (SEC-03), so the goroutine and connection are held until the
client goes away. A few thousand such connections is an inexpensive denial of service.

**Fix sketch:** for amplification, coalesce on the read side too — a short debounce per poll, or a
single per-poll reader that fans the result out to subscribers instead of one query per subscriber.
ADR 0003 already contemplates the second shape. For pinning, set a write deadline per frame via
`http.ResponseController`.

### SEC-05 — No CSRF defense, and `Content-Type` is not validated

**Severity:** Medium — **Static**
**Location:** [`internal/http/handlers.go:73`](../internal/http/handlers.go), [`internal/http/handlers.go:135`](../internal/http/handlers.go)

Neither POST handler checks `Content-Type`, `Origin`, or `Sec-Fetch-Site`, and there is no CSRF
token. `json.Decoder` parses whatever the body contains regardless of the declared type, so a
cross-origin `<form enctype="text/plain">` — which browsers send without a CORS preflight — reaches
both endpoints from any attacker-controlled page.

The interaction with `SameSite=Lax` is worth stating precisely, because it inverts the usual
analysis. Lax cookies are not sent on cross-site POSTs, so the forged request arrives *without*
`voter_token`. The `voterCookie` middleware then mints a fresh token
([`middleware.go:24`](../internal/http/middleware.go)) and the vote is recorded under it. Repeating
the forged POST yields another fresh token each time, since the cookie is still never sent
cross-site. One victim page-view can therefore cast unlimited votes in a poll, and `SameSite=Lax`
makes this easier rather than harder.

Honest impact assessment: today this buys an attacker little over `curl`, because there is no
authentication to ride on and no rate limiting to evade. It becomes material the moment SEC-02 is
addressed with per-IP limits — forged requests come from victims' IPs, which is exactly what
IP-based limits cannot distinguish. Fixing SEC-02 without fixing this leaves a bypass in place.

**Fix sketch:** require `Content-Type: application/json` on both POST endpoints (a
`text/plain` form post then cannot reach them at all without a preflight), and reject requests whose
`Sec-Fetch-Site` is `cross-site`. Both are cheap and need no token infrastructure.

### SEC-06 — Voter cookie lacks `Secure` and the `__Host-` prefix

**Severity:** Medium — **Static**
**Location:** [`internal/http/middleware.go:30`](../internal/http/middleware.go)

```go
http.SetCookie(w, &http.Cookie{
    Name:     voterCookieName,
    Value:    token,
    Path:     "/",
    HttpOnly: true,
    SameSite: http.SameSiteLaxMode,
    MaxAge:   int((365 * 24 * 3600)),
})
```

`HttpOnly` and `SameSite` are set; `Secure` is not. Over plaintext HTTP the token is readable by any
network observer, and more importantly it is *writable* by an active network attacker — cookie
forcing works even against an HTTPS origin, because a cookie set over HTTP for the same host is sent
on HTTPS requests too. That gives a griefing vector specific to this application: force a victim's
`voter_token` to a value that has already voted in a poll, and the victim's own vote is silently
rejected with 409 "already voted". The victim has no way to tell this happened.

The one-year `MaxAge` extends the window and gives the token a tracking lifetime longer than the
polls it deduplicates.

**Fix sketch:** set `Secure: true` and rename the cookie to `__Host-voter_token`, which browsers
accept only with `Secure`, `Path=/`, and no `Domain` — all three already hold. Shorten `MaxAge` to
something matched to poll lifetime.

### SEC-07 — Capability URLs with no referrer or indexing controls

**Severity:** Medium — **Static**
**Location:** [`internal/http/views.go:19`](../internal/http/views.go), [`internal/http/router.go:24`](../internal/http/router.go)

Access control is entirely "knowing the poll ID". The ID has roughly 59 bits of entropy
(62<sup>10</sup>), so guessing is not the concern. Leaking is:

- No `Referrer-Policy` header. The current pages load no external resources and contain no outbound
  links, so nothing leaks today. That property is one added `<img>`, analytics snippet, or user-
  supplied link away from breaking, and when it breaks the full capability URL goes to a third party
  in the `Referer` header. A header set now costs nothing and survives that change.
- No `X-Robots-Tag` or `<meta name="robots" content="noindex">`. A poll URL pasted anywhere a crawler
  reaches becomes publicly indexed.
- Poll URLs land in browser history, in any intercepting proxy's logs, and in shared-link previews.

This is inherent to the capability-URL model and is a reasonable choice for casual polls. The finding
is the absence of the cheap mitigations, not the model.

**Fix sketch:** `Referrer-Policy: no-referrer` and `X-Robots-Tag: noindex` in a headers middleware.

### SEC-08 — No security headers

**Severity:** Medium — **Static**
**Location:** [`internal/http/views.go:26`](../internal/http/views.go)

Responses carry only `Content-Type`. Missing, in rough order of value here:

- **`X-Frame-Options: DENY` / `frame-ancestors 'none'`.** The vote page is a set of large single-
  click buttons that immediately submit a vote. That is close to an ideal clickjacking target: frame
  `/p/{id}` invisibly, overlay bait, harvest votes from every visitor. This is the most concrete of
  the missing headers for this particular application.
- **`Content-Security-Policy`.** No XSS exists today (see the positives section), so CSP would be
  defense in depth. Worth noting that the three pages use inline `<script>` blocks, so a strict
  policy needs hashes or nonces, or the scripts moved to files served like `app.js`. Doing it later
  is more work than doing it now.
- **`X-Content-Type-Options: nosniff`.** Every response sets an explicit `Content-Type`, which limits
  the practical impact, and sniffing protections are cheap.
- **`Permissions-Policy`.** No sensitive APIs are used; a deny-all policy is free.

**Fix sketch:** one headers middleware wrapping the mux in `NewRouter`, next to `voterCookie`.

### SEC-09 — Known vulnerabilities in the Go toolchain and `golang.org/x/text`

**Severity:** Medium — **Verified** (`govulncheck`, 2026-08-16)
**Location:** [`go.mod`](../go.mod), [`Dockerfile:16`](../Dockerfile)

Five vulnerabilities are reachable from this code (three further ones are present but not called):

| ID | Component | Present | Fixed in | Reached via |
|---|---|---|---|---|
| GO-2026-6090 | `crypto/tls` | go1.26.5 | go1.26.6 | `ListenAndServe` |
| GO-2026-6089 | `net/http` | go1.26.5 | go1.26.6 | `ListenAndServe` — `ReadHeaderTimeout` not applied on the unencrypted HTTP/2 check |
| GO-2026-6088 | `encoding/xml` | go1.26.5 | go1.26.6 | `pgx.connRow.Scan` |
| GO-2026-5972 | `encoding/asn1` | go1.26.5 | go1.26.6 | `pgxpool.Pool.Close` |
| GO-2026-5970 | `golang.org/x/text` | v0.29.0 | v0.39.0 | `pgxpool.New` (infinite loop on invalid input) |

GO-2026-6089 compounds SEC-03: the single timeout the server does set is not reliably applied on the
current toolchain.

**Fix sketch:** bump `go 1.26.5` in `go.mod` and `golang:1.26-alpine` in the Dockerfile to a 1.26.6+
toolchain, and `go get golang.org/x/text@v0.39.0`. Add `govulncheck ./...` to CI so this is caught
without a manual audit.

### SEC-10 — Application database role owns the schema and runs DDL at startup

**Severity:** Medium — **Static**
**Location:** [`internal/store/store.go:55`](../internal/store/store.go), [`docker-compose.yml:5`](../docker-compose.yml)

`applyMigrations` re-executes every embedded `.sql` file on every startup, using the same pooled
connection the request path uses. The role in `DATABASE_URL` therefore needs `CREATE TABLE`,
`ALTER TABLE`, and `DROP CONSTRAINT` rights permanently — `0002_option_poll_fk.sql` issues
`ALTER TABLE ... DROP CONSTRAINT`. In compose, that role is `poll`, the database owner.

Least privilege is not in force: any code-execution or connection-string compromise gets schema
control, not merely data access. The single-binary, no-migration-runner design is a documented
trade-off in [`migrations/migrations.go`](../migrations/migrations.go); the privilege consequence is
not discussed there.

Worth stating clearly: this is not a SQL injection risk. `pool.Exec(ctx, string(sql))` at
[`store.go:72`](../internal/store/store.go) is the only place a SQL string is not parameterized, and
its input comes from `//go:embed`, so it is fixed at compile time. It becomes a critical finding only
if migrations ever move to disk, an environment variable, or an admin endpoint.

**Fix sketch:** split roles — a migration role with DDL rights used by a separate startup step or
init container, and a runtime role holding only `SELECT`/`INSERT` on the three tables. Enforce that
the embed stays the sole migration source.

---

## Low

### SEC-11 — Unencrypted database default with hardcoded credentials

**Severity:** Low — **Static**
**Location:** [`cmd/server/main.go:29`](../cmd/server/main.go)

```go
dbURL := envOr("DATABASE_URL", "postgres://poll:poll@localhost:5432/poll_tergeist?sslmode=disable")
```

The fallback embeds credentials and disables TLS. A deployment that fails to set `DATABASE_URL` does
not fail loudly — it starts and attempts a plaintext connection with known credentials. In practice
it would fail to connect in most production topologies, so the immediate risk is low; the pattern is
fail-open on missing configuration, and the same string ships in the public repository. Rated low
rather than dismissed because the compiled-in default is what a misconfigured deploy silently uses.

**Fix sketch:** require `DATABASE_URL` in non-development builds and exit with a clear error when it
is absent. Keep the convenience default behind an explicit `DEV=1` or a separate compose-only
override.

### SEC-12 — `voter_token` accepted from the client without validation

**Severity:** Low — **Static**
**Location:** [`internal/http/middleware.go:44`](../internal/http/middleware.go), [`migrations/0001_init.sql:24`](../migrations/0001_init.sql)

`voterTokenFrom` returns the cookie value verbatim. There is no length check, no charset check, and
no `CHECK` constraint on the `votes.voter_token` column, although the neighbouring `question` and
`label` columns both have one. The value is bound as a query parameter, so this is not injection.
Two consequences:

- Tokens are attacker-chosen rather than server-issued. That is inherent to a cookie the client
  holds, and it means the value can be any string up to the header size limit (Go's default
  `MaxHeaderBytes` is 1 MB), not the 32-char base62 the server issues.
- A token longer than roughly 2.7 KB exceeds Postgres's btree index row limit for
  `UNIQUE (poll_id, voter_token)`, so the INSERT errors and the handler returns 500. A trivially
  triggerable 500 that is neither logged (SEC-14) nor rate limited (SEC-02).

**Fix sketch:** validate the cookie in `voterCookie` — if it is not exactly 32 base62 characters,
treat it as absent and issue a fresh token. Add a matching `CHECK (length(voter_token) = 32)` for
defense in depth.

### SEC-13 — Modulo bias in random ID generation

**Severity:** Low — **Static**
**Location:** [`internal/poll/poll.go:69`](../internal/poll/poll.go)

```go
for i, v := range b {
    b[i] = idAlphabet[int(v)%len(idAlphabet)]
}
```

256 is not a multiple of 62. Byte values 0–7 map to their character twice more often than the rest,
so the first eight alphabet characters (`0`–`7`) occur with probability 5/256 rather than 4/256. The
entropy loss is small — roughly 59.4 bits instead of 59.5 for a 10-character poll ID, and about 190
bits for the 32-character voter token — and neither is close to a practical guessing threshold. It is
listed because the fix is three lines and because the same helper would be reused if a
security-sensitive token is ever added.

**Fix sketch:** rejection sampling (discard bytes ≥ 248), or `rand.Text()` from the standard library
if a base32 alphabet is acceptable.

### SEC-14 — No request or error logging

**Severity:** Low — **Static**
**Location:** [`internal/http/handlers.go:64`](../internal/http/handlers.go), [`cmd/server/main.go`](../cmd/server/main.go)

`writeError` returns a generic message to the client and discards the underlying error. Nothing is
written to stdout for any request or any 5xx. The only log lines in the binary are startup and
shutdown. Every error path in `store` wraps its cause carefully with `%w`, and no consumer ever reads
those wrapped errors.

Client-facing messages are correctly generic, which is good practice — the finding is that the detail
is dropped entirely rather than logged server-side. Consequences: abuse (SEC-01, SEC-02, SEC-12) is
invisible while it happens and unreconstructable afterwards, and a genuine bug surfaces only as a
user complaint.

**Fix sketch:** `log/slog` with a request-logging middleware (method, path, status, duration, no
cookie values), and log the wrapped error at the point `writeError` is called with a 5xx.

### SEC-15 — Container and compose hardening gaps

**Severity:** Low — **Static**
**Location:** [`Dockerfile`](../Dockerfile), [`docker-compose.yml`](../docker-compose.yml)

The Dockerfile gets the important parts right: multi-stage, `CGO_ENABLED=0`, non-root `USER app`, no
shell in the entrypoint. Remaining gaps, all minor:

- Base images are tag-pinned (`alpine:3.20`, `golang:1.26-alpine`, `postgres:17-alpine`) rather than
  digest-pinned, so builds are not reproducible. `alpine:3.20` also trails current releases and
  carries whatever unpatched CVEs its `ca-certificates` chain has accumulated.
- No `read_only: true`, no `cap_drop: [ALL]`, no `security_opt: [no-new-privileges:true]`. The binary
  needs no writable filesystem and no capabilities.
- No `mem_limit`/`pids_limit`, which is what turns SEC-01 from "the container dies" into "the host
  degrades".
- Postgres is published on host `5432` with `poll:poll`. Fine for local development, and a real
  exposure if the compose file is ever used on a shared or cloud host. It does not need to be
  published at all for the app to reach it.
- No `HEALTHCHECK` for the app service (the db service has one).

**Fix sketch:** digest-pin images, add `read_only`, `cap_drop`, `no-new-privileges`, and memory
limits to the app service, and drop the `ports` mapping on `db`.

### SEC-16 — Voter token reused across polls, stored in plaintext

**Severity:** Low (disclosed in part) — **Static**
**Location:** [`migrations/0001_init.sql:24`](../migrations/0001_init.sql)

The README already documents that one raw token is reused for every poll a browser votes in, that
this permits cross-poll correlation by anyone with `votes` table access, and that a poll-scoped
derivation such as `HMAC(secret, pollID)` is the evolution. That analysis is accurate and needs no
repetition.

Two details it does not cover: the token is stored in plaintext rather than hashed, so a read-only
database leak yields values that can be replayed as cookies to impersonate a voter's dedupe identity;
and the token is never rotated, so its correlation window is the cookie's one-year `MaxAge`
(SEC-06).

**Fix sketch:** as documented in the README, plus storing only a hash of the derived per-poll
identifier.

### SEC-17 — SSE subscription precedes the existence check

**Severity:** Low — **Static**
**Location:** [`internal/http/stream.go:19`](../internal/http/stream.go)

`hub.Subscribe(id)` runs before `loadPollAndTally` validates that the poll exists, so a client can
allocate hub map entries for arbitrary IDs. The ordering is deliberate and correct — ADR 0003
explains that subscribing first is what prevents a vote committed mid-handshake from being missed —
and `defer unsubscribe()` cleans up on every path, so the growth is bounded by concurrent connections
rather than by total requests. Noted for completeness because it interacts with the absent connection
cap (SEC-02); on its own it is not exploitable.

**Fix sketch:** none needed independently. Fix the connection cap.

### SEC-18 — Poll ID from the path is unvalidated before reaching the database

**Severity:** Low — **Static**
**Location:** [`internal/http/handlers.go:102`](../internal/http/handlers.go), [`internal/http/handlers.go:136`](../internal/http/handlers.go), [`internal/http/stream.go:17`](../internal/http/stream.go)

`r.PathValue("id")` is passed straight to the store with no length or charset check. It is
parameterized, so there is no injection, and a mismatched ID yields a clean 404. The effect is that
arbitrarily long path segments (bounded by the 1 MB header limit) become query parameters, and every
malformed ID costs a database round trip that a five-character check in the handler would have
avoided.

**Fix sketch:** reject IDs that are not exactly 10 base62 characters before touching the store.

### SEC-19 — Client-side validation is not the enforcement boundary

**Severity:** Low (informational) — **Static**
**Location:** [`web/create/index.html:76`](../web/create/index.html)

`MIN_OPTIONS`/`MAX_OPTIONS` and the `maxlength` attributes are duplicated in the browser. Every one of
those constraints is independently enforced by `poll.ValidateCreate` and by `CHECK` constraints in
the schema, so this is correct layering. Recorded so a future reader does not mistake the client
constants for the boundary, and because option *count* is enforced in application code only — the
schema cannot express it, as [`poll.go:85`](../internal/poll/poll.go) explains.

### SEC-20 — Duplicate option labels accepted

**Severity:** Low (informational) — **Static**
**Location:** [`internal/poll/poll.go:92`](../internal/poll/poll.go)

`ValidateCreate` permits two options with the same label. `UNIQUE (poll_id, position)` keeps the rows
distinct, so the tally stays correct, and voters see two identical buttons. This is a UX and
poll-integrity oddity — a poll author can split an opponent's vote by listing the same option twice —
rather than a security defect in the usual sense. Included because the application's core value is
tally trustworthiness.

---

## Verified as sound

Recorded so a later reviewer does not re-derive them, and so a regression is visible as a change to a
documented property.

- **SQL injection: not present.** Every statement in [`internal/store/store.go`](../internal/store/store.go)
  uses `$n` bind parameters. No `fmt.Sprintf`, no concatenation, no interpolated identifiers. The
  attacker-controlled inputs — poll question, option labels, path-derived poll ID, and the fully
  attacker-controlled `voter_token` cookie — are all bound. The sole unparameterized `Exec`
  ([`store.go:72`](../internal/store/store.go)) takes compile-time embedded input. Note that a future
  `default_query_exec_mode=simple` in the connection string (a common PgBouncer accommodation) would
  swap pgx's protocol-level parameter separation for client-side escaping — still safe, a weaker
  guarantee.
- **Cross-site scripting: not present.** All user-controlled values reach the DOM via `textContent`
  ([`vote/index.html:73,82`](../web/vote/index.html), [`results/index.html:93,94,116`](../web/results/index.html)).
  The one `innerHTML` assignment ([`results/index.html:83`](../web/results/index.html)) writes a
  static literal with no interpolation, and the interpolated `bar-${opt.id}` element ID comes from a
  server-side `int64`.
- **Path traversal: not reachable.** `views.asset` is only ever constructed with hardcoded path
  constants in [`router.go:24-27`](../internal/http/router.go); no request data reaches
  `fs.ReadFile`, and the filesystem is an `embed.FS` containing only the three pages and `app.js`.
- **Vote dedupe races: closed at the schema.** `UNIQUE (poll_id, voter_token)` plus the composite
  `FOREIGN KEY (poll_id, option_id)` from `0002_option_poll_fk.sql` make a cross-poll option/poll
  mismatch impossible regardless of which code inserts. The `WHERE EXISTS` guard in `InsertVote`
  turns the mismatch into a clean 400 in the same round trip. Both layers were checked.
- **Randomness source is correct.** `crypto/rand`, not `math/rand`, in
  [`poll.go:66`](../internal/poll/poll.go), with errors propagated rather than ignored. See SEC-13
  for the bias caveat.
- **Error messages do not leak internals.** `writeError` emits fixed strings; wrapped causes never
  reach the client. See SEC-14 for the flip side.
- **No CORS headers are set**, so browsers block cross-origin reads of the API by default. Combined
  with SEC-05, note this stops cross-origin *reading*, not cross-origin *writing*.
- **Container runs as a non-root user** with a static binary and no shell in the entrypoint.
- **No secrets in the repository or in git history.** `plan.md`, `decision-log.md`,
  `NOTES-private.md`, and `docs/challenge-brief.md` are gitignored and dockerignored, and
  `git log --all` confirms none was ever committed. The only credentials present are the
  development-only `poll:poll` pair (SEC-11).

## Suggested order of work

Not a plan, and no code was changed. If these are picked up, this ordering front-loads the cheap
structural wins:

1. **SEC-09** — toolchain and `x/text` bump. Two commands, removes five reachable CVEs.
2. **SEC-01** — `MaxBytesReader` in both handlers. A few lines, closes the highest-impact finding.
3. **SEC-08**, **SEC-07**, **SEC-06** — one headers middleware plus the `Secure` cookie flag. Also a
   few lines, and clickjacking on the vote page is the most directly exploitable of the three.
4. **SEC-03** — server timeouts.
5. **SEC-02** — rate limiting. The largest piece of real work, and it should land together with
   **SEC-05**, since per-IP limits without the `Content-Type`/`Sec-Fetch-Site` check are bypassable
   through victims' browsers.
6. **SEC-14** — logging, so the effect of items 1–5 is observable.
7. **SEC-10**, **SEC-11**, **SEC-15** — deployment posture, relevant when the "no deploy" gap in the
   README is closed.

Adding `govulncheck ./...` to CI is what keeps SEC-09 from recurring.

## Method

Manual review of all 18 source files (Go, SQL, HTML, JavaScript), the Dockerfile, the compose file,
and the ignore files, plus `govulncheck` against the module graph. The five ADRs and the README were
read first to separate disclosed trade-offs from gaps.

No dynamic testing was performed: no Postgres instance and no Docker daemon were available on the
audit host, so no finding was reproduced against a running server. The findings most worth
confirming empirically before acting are SEC-01 (actual allocation ceiling), SEC-04 (goroutine
pinning under a non-reading client), and SEC-12 (the btree index row-size limit producing a 500).

No automated static analysis beyond `govulncheck` — `gosec` and `semgrep` were not available on the
host. Running both would be a reasonable cross-check of this review.

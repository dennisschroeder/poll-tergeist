# How this was built: an AI-agent workflow

This repo wasn't produced by pasting a spec into a chat window and accepting whatever came
back. It was built through a structured workflow: a plan fixed before any code, an execution
graph drawn before implementation, decisions gated on human review, and an independent pass
checking the result before calling it done. This page shows that process, not just the output.

## The execution graph

```mermaid
flowchart TD
    A[Plan: architecture, data model, ADR structure<br/>fixed before any code was written] --> B[Execution graph drawn<br/>and shown before implementation]
    B --> C[Scaffold: repo, module,<br/>compose, migration]

    C --> D1[Backend: domain, store, hub,<br/>HTTP, wiring]
    C --> D2[Frontend: 3 views + shared JS]

    D1 --> E[Build, vet, format]
    D2 --> E

    E --> F[Tests written:<br/>concurrency + handler]
    F --> G{Green under -race?}
    G -->|no — ordering bug found| F
    G -->|yes| H[Manual smoke test<br/>in a real browser]

    H --> I[README drafted]
    I --> J[["ADR loop — human gate<br/>one ADR at a time"]]

    J --> K[Draft ADR] --> L[Present in full] --> M{Approved?}
    M -->|edits requested| K
    M -->|approved| N[Commit]
    N -.->|next ADR| J

    J --> O[Independent code review<br/>Standards axis + Spec axis, parallel]
    O --> P{Findings?}
    P -->|2 minor, duplication| Q[Fix + re-verify]
    P -->|none| R
    Q --> R[Attempted external second opinion]
    R --> S{Tool reachable?}
    S -->|no — auth broken| T[Abandoned, logged, moved on]
    S -->|yes| U[Incorporated]

    T --> V[Containerize: Dockerfile + compose]
    U --> V
    V --> W[Re-verified in a real browser<br/>not just curl]
    W --> X[Privacy check: git status --ignored]
    X --> Y[Public repo created, pushed]

    Y -.->|manually triggered, any time,<br/>not part of the default loop| Z{{"Cross-model-provider review<br/>a different vendor's model audits the diff"}}
    Z --> AA{Findings?}
    AA -->|yes| AB[Incorporate + re-verify]
    AA -->|no, or unreachable| AC[Confirms existing findings —<br/>not a blocker either way]
    AB -.->|feeds back in| J

    classDef gate fill:#3b2f7a,stroke:#8b7fd8,color:#fff
    classDef bug fill:#5a2a2a,stroke:#d87f7f,color:#fff
    classDef manual fill:#3a2f1f,stroke:#d8a24f,color:#fff,stroke-dasharray: 4 3
    class J,M gate
    class G,S bug
    class Z,AB manual
```

Two things worth reading closely, because they're the point:

- **The back-edges are real, not decorative.** The tests loop caught a genuine ordering bug (a
  test truncated its database tables before migrations had created them). The ADR loop's edit
  cycle isn't a formality — every one of the four ADRs in this repo went through at least one
  real revision before being approved.
- **The "abandoned" branch stayed in the graph.** An attempt to get an independent review from a
  second model failed on a broken auth session in the tool, was retried twice, and was then
  deliberately dropped rather than forced. A workflow that only shows the parts that worked isn't
  showing the workflow.
- **The cross-model-provider review at the end is a standing capability, not that one attempt.**
  It's drawn separately from the failed attempt above and marked manually triggered because that's
  what it is: not a stage the pipeline runs on its own, but a second opinion from a different
  vendor's model, invoked on request, at whatever point it's wanted — including, as it happened
  here, after everything else was already done and shipped.

## What the graph is actually demonstrating

**Plan before code, not code then plan.** The architecture, data model, and even the *structure*
ADRs would take were fixed in writing before implementation started. The execution graph itself
was presented and could have been corrected before a single file was touched.

**Parallel work only where it's genuinely safe.** The backend and frontend were built
concurrently by different workers because they touch disjoint files and share a frozen API
contract — not because parallelism is impressive. Nothing else in this build was parallelized;
most of it is too interdependent to split safely.

**Every architectural decision is human-gated, not just human-visible.** Four ADRs, each drafted,
presented in full, and revised — sometimes more than once — before being committed. Nothing was
accepted on a first draft. One ADR was restructured entirely mid-review after a spotted flaw in
how it grouped two orthogonal decisions.

**An independent reviewer, not just self-review.** A separate pass audited the finished code
against its own documentation before it was called done, and found two real (if minor) issues
that got fixed and re-verified — the model checking its own work is a weaker signal than a
second, differently-motivated pass finding something.

**Cross-model-provider review is available, not automatic.** The review above stays within one
model family — a second, same-provider pass auditing the first. A different vendor's model
auditing the same diff is a genuinely different kind of check, but it's a manually triggered one:
invoked when a second, independently-trained opinion is specifically wanted, not run by default
on every change. Tool reliability across providers also varies enough in practice that forcing it
into the automatic path would make the automatic path only as reliable as its flakiest dependency.

**Correctness claims are checked, not assumed.** "It builds" and "the tests pass" were verified
by actually running them, repeatedly, including through a full container rebuild. A claim that a
containerized app worked based only on `curl` from inside the container was treated as
insufficient until it was confirmed in an actual browser.

**A written decision record, not a memory of one.** Every point where a human decision changed
the direction of the build — not just approved it — is logged as it happened, in the same shape
every time: what was proposed, what was chosen, and whether that was an override or an original
call nobody had offered as an option.

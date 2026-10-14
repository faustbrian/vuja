# Isolated long-session and suggestion evidence

These are library/model checks, not a Vuja build or live-terminal acceptance.
They use fictional commands and task-owned databases. No installed history,
credentials, shell startup, Vuja process or external provider is exercised.
Agent runs must use the sanitized environment and disposable caches required
by AGENTS.md. Timings below describe one host, not a CI pass/fail threshold.

## Ranking latency

Measured on 2026-10-14 with Go 1.27.2, darwin/arm64, Apple M4 Max.
The measured local source candidate is
`ddec341d9633e57cb15b3fe9e06bc189e56b28c0`.

`BenchmarkSuggestionEnrichmentFixture` prepares 200 directory candidates,
1,000 unrelated history candidates and 100 local frecency entries outside
the timer. It measures candidate collection, deduplication and scoring for
`cd`; it does not call live providers, query SQLite, paint the terminal or
measure input-to-first-paint latency.

```sh
go test ./root -run '^$' -bench '^BenchmarkSuggestionEnrichmentFixture$' \
  -benchtime=1x -count=102 -benchmem -timeout=120s
```

Discard the first operation as warm-up, sort the remaining 101 `ns/op`
samples, and use nearest ranks `ceil(0.50*n)` and `ceil(0.95*n)`—ranks 51
and 96. This measures individual one-operation benchmark samples, not
percentiles of averaged batches. The Go benchmark harness performs collection
between samples; these are not an interactive production latency distribution.

| Scope | Samples | p50 | p95 | Minimum | Maximum |
| --- | --- | --- | --- | --- | --- |
| In-memory ranking fixture | 101 | 0.688 ms | 0.824 ms | 0.600 ms | 1.028 ms |

Host load was not controlled. No before/after speedup is claimed, and this
does not explain a user's reported suggestion delay. Provider discovery,
history lookup, asynchronous scheduling and rendering require separate
evidence before attributing such a delay to ranking.

## Large canonical-history publication

```sh
go test ./root -run '^$' \
  -bench '^BenchmarkCanonicalHistoryColdStoreLoad100K$' \
  -benchtime=1x -count=3 -benchmem -timeout=180s
```

The existing fixture seeds 100,000 completed imported events through the real
store, with roughly 10,000 distinct repeated commands and 40 distinct SSH
commands. `atuin` is a fixture provenance label only: no Atuin process or
database is used. Setup, schema creation and seeding are outside the timer.

The measured operation is `publishCanonicalStoreHistory`: query the change
boundary and all events, filter/convert entries, then construct and publish
the canonical recall/search generation. Despite the benchmark's historical
name, the SQLite store is already open and OS caches are not flushed. It is
not a cold process-start measurement or a per-submission/per-keystroke cost.

| Timed scope | Three-sample elapsed range | Cumulative allocation per operation | Allocation objects per operation |
| --- | --- | --- | --- |
| Full publication of 100,000 stored events | 778–786 ms | 423.43 MB | 5.742 million |

Cumulative allocation is not retained heap, peak memory or RSS. This is a
single-writer fixture, not evidence about concurrent-pane latency, arbitrary
database sizes or forced termination. No database optimization or acceptable
production budget is inferred from this baseline.

## Ordering and recovery assertions

`TestSuggestionRankingTiesIgnoreProviderEnumeration` uses 64 independently
specified, equal-evidence command/identity pairs. It permutes their provider
enumeration 128 times with a fixed seed, then exercises real collection,
deduplication and scoring. Every permutation must preserve the known lexical
command order and its associated identities. A disposable counterfactual
removing the scorer's lexical tie-break fails on the first permutation.
The production ranking model is unchanged.

The ordering test passed three race-enabled repetitions. Existing pipeline
generation/cache tests and scorer tie/index-priority tests passed 25
race-enabled repetitions. These are focused invariants, not all provider,
configuration or asynchronous interaction combinations.

Existing publication-lock coordination, durable journal fallback/idempotent
replay and store-reopen SSH recall tests passed three race-enabled
repetitions. Rich-history bounded ordering passed 25 race-enabled repetitions
with 1,000 distinct fictional SSH entries, repeated-result equality, and
independently specified result count and newest/oldest boundaries. The new
permutation test and these recovery checks were
run against isolated source candidate
`dbb07d3cf330f0dbb5c037d1d8a3b7df4df328bd`. Relevant ranking/publication
production sources match the measured candidate above.

These checks do not establish actual concurrent SQLite-writer latency, real
forced termination, terminal session restoration or multi-day behavior.
Ordinary physical drag/copy, window/pane movement, native application handoff
and the supported iTerm2/Ghostty shell matrix remain Brian-only acceptance.

## Independent SQLite writer contention

The additional contention fixture holds `BEGIN IMMEDIATE` on an independent
connection until the production submission's durable acknowledgement returns.
It verifies the private recovery record, absence of a partial canonical row,
two idempotent replays, and exactly one execution after reopening the store.
This models a competing SQLite writer, not just Vuja's publication mutex.

Before the fix, three acknowledgement samples were 5.107–5.120 seconds despite
the one-second canonical submission context. Diagnostic-only phase tracing
in a disposable source copy isolated 5.072–5.086 seconds to the initial writer
UPDATE, versus 6–7 ms for the failure counter and 25–32 ms for journal fallback.
SQLite's connection-local [busy timeout](https://www.sqlite.org/c3ref/busy_timeout.html)
remained 5000 ms while the pinned modernc v1.53.0 driver returned only after
that wait. Context cancellation alone did not shorten it in this fixture.

The fix pins short-budget writes to one connection, caps the busy handler to
the remaining budget, and restores the ordinary policy before pooling it.
Existing transaction ordering is preserved: an already-forgotten or completed
execution can resolve read-only even while another connection owns the writer.
Operations requiring a writer before decision reads retain their own guard.
Ordinary background writes, initialization and read-only transactions retain
their existing policy. A connection that cannot be restored is discarded.

Three post-fix acknowledgement samples were 1.047–1.058 seconds with journal
durability and exactly-once replay/reopen assertions passing. This is not a
hard one-second end-to-end guarantee: scheduling, connection initialization,
cleanup, counter/journal locking and filesystem sync remain separate costs.
Rounding the busy budget down may return SQLite busy before the Go deadline;
this deliberately takes the existing durable fallback rather than extending
the caller's lock-wait budget.

A trigger inside real submission, completion and compatibility writes observes
SQLite's actual policy. It fails before the fix because those writes still
use 5000 ms under a 1000 ms budget. Successful, failed and independently
blocked writes check that all four pool connections retain the ordinary
5000 ms timeout and FULL synchronization. Contention also exercises a fresh
physical connection. No timing value is a test pass/fail threshold.

A separate held-writer regression covers delayed completions for already
forgotten and already-completed executions. They remain read-only no-ops:
no failure-counter increment, recovery file or unhealthy observer event. The
test rejected an intermediate change that acquired the writer before those
decisions; preserving the original ordering fixes both cases.

### Uncontended cost comparison

`BenchmarkHistorySubmissionWriteBudget` excludes store creation and measures
the complete FULL-sync submission commit, including each interactive context.
The baseline is HEAD `0ec6a964` plus the identical benchmark; the candidate's
code/test tree is `4fcec4e9c23752e17792d81e57fa0ed1c327f4a4`.

```sh
go test ./internal/scoring -run '^$' \
  -bench '^BenchmarkHistorySubmissionWriteBudget$' \
  -benchtime=20x -count=3 -benchmem -timeout=90s
```

Go 1.27.2, darwin/arm64, Apple M4 Max; sequential baseline/candidate runs,
uncontrolled host load and warm OS caches. Each sample averages 20 operations.

| Scope | Baseline three-sample range | Candidate three-sample range | Allocation objects/op |
| --- | --- | --- | --- |
| One-second interactive budget | 212–225 µs/op | 222–235 µs/op | 199–203 → 226–230 |
| Ordinary background write | 209–213 µs/op | 211–248 µs/op | 192 → 193 |

The elapsed ranges overlap; no uncontended latency improvement is claimed.
Interactive connection/policy management adds 27 allocation
objects per operation in this fixture. The benefit established here is the
shorter contended wait, without changing FULL-sync durability or globally
shortening the five-second background policy. Globally lowering that policy
would also change ordinary imports/initialization; returning asynchronously
would introduce unresolved late-write/acknowledgement ownership. Neither is
needed for this fix. Live pane responsiveness and forced termination remain
Brian-only checks, not consequences proven by these measurements.

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

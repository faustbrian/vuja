# Native archive ordering: bounded performance evidence

Native transition ordering must survive partial archive merges, tied clocks,
and delayed completions. SQLite insertion rowids cannot supply that portable
order. All transition consumers use canonical `history_order` and `event_key`;
an ordered native-session index bounds predecessor/successor lookups.

## Reproduction

The isolated real-SQLite benchmark seeds 1,000 or 10,000 executions in one
session/directory, including one interrupted middle execution. It compares
ordinary canonical submission and delayed completion, using identical schema
and data except for the candidate native-session index. Setup is untimed;
completion repairs the actual production exact/skeleton projections. Separate
post-measurement assertions verify retained executions and transition edges.

```sh
go test ./internal/scoring -run '^$' \
  -bench '^BenchmarkHistoryArchivePortableOrder$' \
  -benchtime=1x -count=3 -benchmem -timeout=240s
```

Agent runs additionally follow AGENTS.md isolation: Vuja environment variables
are unset, HOME/XDG/TMPDIR are private, and GOCACHE is disposable and cleaned.
The benchmark does not launch Vuja, a shell integration, PTY or network tool.

## Measured comparison

2026-10-14, Go 1.27.2, darwin/arm64, Apple M4 Max, modernc SQLite from the
repository dependency lock. Three independent one-operation samples per case:

| Fixture / operation | Existing session index only | Ordered native index |
| --- | --- | --- |
| 1,000 / submission | 0.385–0.439 ms | 0.334–0.421 ms |
| 1,000 / late completion | 329–357 ms | 3.61–4.13 ms |
| 10,000 / submission | 0.505–0.639 ms | 0.378–0.505 ms |
| 10,000 / late completion | 43.1–48.3 s | 34.9–51.6 ms |

The baseline is the **proposed portable-order query without the new index**,
not a measurement of a released Vuja version. Its correlated repair query
repeatedly scans/sorts a session for each retained execution. The composite
index `(session_id, history_order, event_key) WHERE imported = 0` supplies that
ordering directly; the older session-only index cannot do so. Reusing rowid
would retain the demonstrated archive correctness defect. Existing indexes
are retained for their other consumers rather than removed speculatively.

In this fixture, occupied logical database pages grew from 647,168 to 688,128
bytes at 1,000 events (+6.3%), and from 4,009,984 to 4,374,528 bytes at 10,000
events (+9.1%). Index creation took 0.77–1.32 ms and 6.23–18.3 ms respectively,
outside the timed operations. Each new native event maintains one additional
index entry. The submission samples show no clear regression at this resolution;
they do not prove zero write or WAL cost. No universal timing assertion is used.

This demonstrates benefit and bounded fixture cost for the actual changed
writer/repair path, not production latency, live terminal responsiveness,
startup cost at arbitrary sizes, or aggregate Vuja performance. Larger and
concurrent-session behavior remains part of the broader compatibility work.

### Completion-path refresh

After adding immutable aggregate scope and portable completion-outcome
tie-breaking, three one-operation samples of the indexed late-completion path
measured 5.27–5.54 ms at 1,000 events and 35.9–57.9 ms at 10,000, on the same
toolchain/hardware. Setup and index creation remain outside the timer, and the
existing transition assertions still run separately. This is a current bounded
cost sample, not a fresh unindexed comparison or a latency distribution;
concurrent host load was not controlled. Reproduce this subset with:

```sh
go test ./internal/scoring -run '^$' \
  -bench '^BenchmarkHistoryArchivePortableOrder$/(1000|10000)/late-completion/indexed=true$' \
  -benchtime=1x -count=3 -benchmem -timeout=180s
```

## Archive preservation preflight

Archive import also checks that existing learning is reconstructible before
replacing projections. Under the existing SQLite writer transaction it runs
the shared rebuild in a savepoint, compares typed semantic rows, then rolls the
probe back before archive insertion. Unattributed legacy learning causes an
atomic refusal instead of guessed counts or silent loss. Row fingerprints are
temporary memory only; local auto-increment projection IDs are not learning
identities. This is management-command work, never per-keystroke work.

```sh
go test ./internal/scoring -run '^$' \
  -bench '^BenchmarkHistoryArchiveMerge$' \
  -benchtime=1x -count=3 -benchmem -timeout=180s
```

On the same machine/toolchain, three one-operation samples of the actual
preservation probe plus one-record merge/rebuild measured 60.5–61.9 ms with
1,000 retained native executions, and 643–657 ms with 10,000. Setup populated
canonical projections outside the timer; postconditions verified execution
counts and retained transition evidence. Reported cumulative allocations were
13.9–14.0 MB/op and 132.1–132.2 MB/op respectively, **not peak live memory**.
The earlier index comparison predates this preflight and the compatibility
insertion trigger; it is not a fresh full-import baseline comparison.
It also predates the immutable-scope and completion-outcome refinements above.

These samples establish bounded fixture cost, not a general latency budget,
100,000-record performance, concurrent-pane behavior, or live-terminal proof.
The archive command retains its existing context deadline and atomic rollback.

## Shared removal protection

The preservation check also protects actual retention, explicit forgetting and
stored counterpart removal. Otherwise command preparation could prune history
and lose compatibility learning before archive admission ever checks it.
It runs before canonical mutation, once per removal transaction; an ordinary
submission or a retention policy that removes nothing does not run the probe.
Navigation comparisons use the saved projection clock, so ordinary expiry of
the 14-day recent-use window is not mistaken for lost attribution. The actual
committed rebuild advances that clock normally.
Recognized incomplete lifecycle-migration projections can require one additional
savepoint probe through the same projector, excluding only the known synthetic
legacy cohort for that comparison. No probe mutations survive the savepoint.
Historical second-resolution compatibility recording is qualified by its
original producer identity, canonical timestamps and the specific projection
row it owned; other rows and modern precision are not rounded. Future
compatibility recording uses the same incremental completion owner and clock
as lifecycle recording. Historical recognition runs only after an exact
comparison fails, so already-normalized rows do not repeatedly pay that cost.

```sh
go test ./internal/scoring -run '^$' \
  -bench '^BenchmarkHistoryLearningRetention$' \
  -benchtime=1x -count=3 -benchmem -timeout=180s
```

On the same Go 1.27.2 / darwin-arm64 / M4 Max environment, the benchmark seeds
complete canonical projections outside the timer, then invokes actual count
retention. Each one-operation sample starts with 1,001 or 10,001 executions;
the removal case retains 1,000 or 10,000. Postconditions check the exact count
and a surviving transition separately. The baseline is the archive candidate
before the shared protection, using the identical benchmark fixture, not a
released-version or full-application comparison.

| Fixture / operation | Before shared protection | With shared protection |
| --- | --- | --- |
| 1,001 / no removal | 0.89–1.03 ms | 0.85–0.91 ms |
| 1,001 / prune one | 24.0–24.1 ms | 58.3–61.4 ms |
| 10,001 / no removal | 8.10–8.47 ms | 8.07–8.15 ms |
| 10,001 / prune one | 258–263 ms | 619–650 ms |

The correction adds substantial cost when a removal really rebuilds learning:
one rollback-only rebuild and semantic-row comparison before the existing
committed rebuild. At 10,001 events, cumulative allocations rise from about
36.7 MB/op to 131.1 MB/op; at 1,001 they rise from 3.8 to 13.7–13.8 MB/op.
These are allocations, **not peak live memory**. The no-removal path retains
its existing scan cost; sample differences do not prove an improvement.

This deliberately trades maintenance cost for a demonstrated data-loss fix,
not a pruning-speed optimization. The probe holds the existing writer
transaction and can delay other writers; it must not be interpreted as input
latency or concurrent-session throughput evidence. The extra legacy-cohort
probe, 100,000-record behavior, peak memory and contention cost remain
unmeasured here. Automatic retention keeps its existing context deadline and
defers safely if reconstruction or the operation cannot complete.

### Historical recording compatibility cost

The same benchmark additionally seeds individual historical `Record` identities
whose canonical timestamps have fractional seconds. `legacy-seconds` gives their
three successful-completion projections the old SQLite second-resolution dates;
`legacy-normalized` retains the precise canonical dates produced by a successful
rebuild. These records have no observed native session, so the assertions also
require that no synthetic transition appears. They are not directly comparable
to the session-bearing modern fixture above.

Three one-operation samples compare eager historical qualification with the
conditional implementation on identical stores and toolchain:

| Fixture / retained projection | Eager qualification | Conditional qualification |
| --- | --- | --- |
| 1,001 / seconds | 45.4–46.1 ms | 50.3–50.8 ms |
| 1,001 / normalized | 44.5–46.3 ms | 41.2–41.5 ms |
| 10,001 / seconds | 474–487 ms | 527–549 ms |
| 10,001 / normalized | 473–477 ms | 414–419 ms |

At 10,001 events, cumulative allocations are about 150.6–150.8 MB for initial
second-resolution reconciliation versus 127.5 MB eagerly, and 82.3 MB for
normalized projections versus 128.6–128.9 MB eagerly. These are not peak memory.
The initial mismatch requires another projection-row read, whereas an exact
match skips historical identity scanning and alternative fingerprints. Actual
committed rebuilding normalizes the projection, so later successful removals
use the latter path. The workload's frequency mix and concurrent host load are
unknown: these samples establish the first-use/repeated-maintenance tradeoff,
not a universal overall speedup or a terminal responsiveness claim.

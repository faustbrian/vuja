# Retained output: bounded model evidence

The terminal SDK owns the mutable terminal state and logical soft-wrap
boundaries. Search, copy, export and new checkpoints consume its semantic
projection. Only drawing uses the current pane width. The opt-in reflow path
does not change the resize policy of the main shadow terminal, backdrop,
preview's final-screen decoder or alternate-screen application canvas. A
fictional retained pane in preview follows the same opt-in production policy.

## Workloads and reproduction

These are isolated SDK/model measurements, not Vuja builds or live-terminal
tests. Agent runs sanitize the Vuja/startup environment, use private HOME/XDG
and temporary directories, and remove the task-owned GOCACHE on every exit,
as required by AGENTS.md. The SDK now belongs to the root module as
`internal/vt`, whose tests are included in root-module package selection.
The historical measurements below used the same SDK implementation in its
former nested-module layout; they are not new measurements of packaging.

From the repository root, select only the SDK package:

```sh
go test ./internal/vt -race -count=3
go test ./internal/vt -run '^$' -bench '^BenchmarkRetainedScrollbackBudget$' \
  -benchmem -count=3
go test ./internal/vt -run '^$' -bench '^BenchmarkRetainedSession(Footprint|Resize)$' \
  -benchtime=1x -count=3 -benchmem -timeout=240s
```

The admission benchmark compares ordinary and opt-in logical scrollback with
the same short lines at the same retained-row counts. Setup fills the buffer
outside the timer. A single-line benchmark does not measure Unicode parsing,
whole-screen rendering or resize.

The footprint benchmark creates a fresh 120-column, 12-row emulator with a
10,000-line retention setting, then writes 10,000 or 100,000 identical
124-byte lines. The one-operation measurement includes ingestion and garbage
collection. Before/after live Go heap deltas are sampled after collection;
newest-output and actual text/row-cap assertions run separately. This is not
per-keystroke latency, RSS, peak memory or a full Vuja process measurement.

The resize benchmark seeds complete Unicode lines before timing. Each operation
resizes through 20×8, 1×1, 160×30 and 120×12. After timing, exact semantic text
and completeness must match their original values. A one-column pane is a
deliberate stress case, not a representative normal window.

## Measured model bounds and cost

2026-10-14, Go 1.27.2, darwin/arm64, Apple M4 Max, three one-operation
samples per workload and candidate. The before candidate is
`93170e31ef00c769f7c9c6dc527f665385a8d900`; the optimized code candidate is
`09f5c1ad88ac9d5dc032133153323ac416654ca7`. Both include the same
resize/grapheme-anchor correction and benchmark fixtures. This is a local
candidate comparison, not a released-version comparison. Concurrent host load
was not controlled, including a concurrent isolated consumer check; timings
are descriptive, not a universal latency budget.

| Input lines written | Before live Go heap | Optimized live Go heap | Retained rows in both | Retained text/link/placeholder cost in both |
| --- | --- | --- | --- | --- |
| 10,000 | 243.50–243.54 MB | 132.41–132.46 MB | 8,594 | 1,048,466 bytes |
| 100,000 | 243.50–243.51 MB | 132.41 MB | 8,594 | 1,048,466 bytes |

Completing a logical line now uses exact-capacity cell storage instead of
retaining append-growth slack. The state reaches a plateau in this fixture,
with approximately 46% less live Go heap, but the absolute cost remains
substantial. The 1 MiB budget measures text, hyperlinks and placeholder columns,
**not heap usage**: styled terminal cells and their allocations cost much more
than a byte per column. Vuja can hold both a pane model and an active execution
model, plus bounded frozen snapshots, completed captures and other renderer
state. This table must not be presented as a process-wide memory ceiling.

| Seeded logical rows | Before four-size cycle | Optimized four-size cycle | Before cumulative allocation | Optimized cumulative allocation |
| --- | --- | --- | --- | --- |
| 100 | 2.01–2.51 ms | 0.69–0.99 ms | 20.5 MB | 9.38 MB |
| 1,000 | 24.7–25.6 ms | 9.72–12.2 ms | 199 MB | 88.4 MB |
| 10,000 | 270–294 ms | 111–124 ms | 1.99 GB | 886 MB |

Cumulative allocation is not peak live memory. Projection avoids an immediate
second copy of each physical row; temporary layouts borrow the newly owned
logical cells; and scrollback reconstruction allocates once per logical line
instead of once per temporary column-sized fragment. The visible canvas still
owns independent padded rows. At 10,000 seeded lines the cycle allocates about
56% fewer bytes and about 88% fewer objects (1,680,637–1,680,639 before,
200,567–200,568 after).

Reflow still visits retained cells and creates temporary layout descriptors;
its cost grows with retained content. These results prove neither a
zero-allocation hot path nor smooth live resizing at the largest setting.
There is no noisy wall-clock assertion pretending to enforce such a guarantee.

## v2 stabilization measurement

On 2026-10-25, Go 1.26.10, darwin/arm64, Apple M4 Max (16 logical CPUs,
128 GiB RAM), the same footprint and four-size resize workloads were run with
three one-operation samples. The baseline was `b54ed23c`; the candidate uses
the owned-whole-row transfer in `replaceOwnedRetained`. The SDK files owning
resize were unchanged between that baseline and the subsequent module-path
migration. Both measurements used `GOMAXPROCS=16`. Host load was uncontrolled
(roughly 90 at baseline and 194 at candidate), so timings are descriptive and
must not be interpreted as a reliable latency comparison.

| Seeded logical rows | Baseline allocation / cycle | Candidate allocation / cycle | Baseline objects / cycle | Candidate objects / cycle |
| --- | ---: | ---: | ---: | ---: |
| 100 | 9,382,056 B | 8,071,336 B | 2,297 | 2,137 |
| 1,000 | 88,363,304–88,363,416 B | 72,306,984 B | 20,387–20,388 | 18,427 |
| 10,000 | 885,951,016–885,951,352 B | 722,438,712–722,438,808 B | 200,567–200,570 | 180,608 |

At 10,000 rows this removes about 18% of cumulative resize allocation. The
candidate's four-size cycle took 144–164 ms versus 169–188 ms at baseline,
but the different host loads prevent a latency guarantee. Profiles include
setup and final semantic comparisons as well as resize; their total bytes
must not be substituted for the timed benchmark's `B/op`.

The owning resize operation already creates a defensive logical projection.
A complete single-fragment historical line can take ownership of that new
storage rather than clone every styled cell a second time. Wrapped, partial
and padded fragments still reconstruct their storage, and the new slice's
capacity cannot reach neighboring cells. The live canvas and externally
returned projections remain independent. A focused allocation regression
distinguishes per-row cell cloning from metadata-array growth; it is not a
universal memory ceiling.

The footprint workload still plateaus at approximately 132.41–132.45 MB of
live Go heap for both 10,000 and 100,000 input lines, with 8,594 retained rows
and 1,048,466 bytes of accounted text/link/placeholder content. This change
reduces transient resize work, **not** steady-state retained heap or measured
process RSS. Full SDK tests, targeted race tests and semantic-text assertions
accompany the measurements; compositor consumers and live-terminal acceptance
remain separate checks.

## Correctness and remaining boundaries

Separate regressions exercise narrow/wide and height changes, pending and saved
cursors, erased/overwritten cells, hidden tails, actual blank lines, Unicode
and fragmented reads, defensive projections, mutable public aliases,
alternate-screen separation, frozen reading anchors and real retention
eviction. The root compositor fixtures additionally exercise search,
execution-local copy/export, checkpoint capture and non-execution input
ownership. SDK and root race checks are distinct from these benchmark samples.

Ordinary drag selection, clipboard permissions, window movement, forced
termination and multi-day behavior require the
[live terminal acceptance matrix](terminal-compatibility.md). Passing model
checks or reaching a bounded fixture plateau does not close those cases.

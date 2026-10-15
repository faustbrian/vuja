# Terminal dependency policy

Vuja treats its terminal emulator dependencies as a release-critical boundary.
External modules are pinned to exact tagged or pseudo-versions in `go.mod`;
the internal SDK copy is pinned to the upstream revision recorded below and
reviewed as tracked source. Automated dependency updates must not move these
boundaries without the terminal regression and real-terminal gates in
`RELEASE.md`.

## Pinned revisions

- `x/vt` source: `v0.1.0`, upstream revision
  `43427fdbad12fde220b737fc36303291a5ce4602`, replaces
  `v0.0.0-20260727090823-41c9e6be3365`. Actual builds use the tracked patched
  package in `internal/vt`, not an unmodified upstream module release.
- `ultraviolet`: unchanged at
  `v0.0.0-20260303162955-0b88c25f3fff`.

The `x/vt` source delta corrects OSC 8 URL/parameter fields and changes
grapheme ingestion, full-screen drawing and resolved key text. It also adds
cursor methods to the upstream `Terminal` interface; Vuja does not implement
that interface or call the changed drawing/key helpers. The update does not
include the stale-margin containment fixes described below. Vuja's local
clamping and recovery boundaries remain necessary, and all adoption gates
below still apply.

## Local retained-output patch

The root-owned internal package preserves upstream's MIT license, source
provenance and existing interfaces. Its dependencies are selected by the root
`go.mod`; no nested module, relative replacement or external fork is required.
This keeps the SDK source in Go module ZIPs and preserves versioned Go
installation. Its additional concrete emulator
methods opt retained primary screens into reflow and provide a defensive
logical-line projection, actual-eviction reporting and cell-anchor mapping
through resize. `SafeEmulator` wraps these methods with its existing lock.
Public mutable cell/scrollback APIs remain mutable; an outstanding scrollback
alias requires conservative budget recounts. A safe scrollback cell lookup
takes the exclusive lock because exposing an alias updates that bookkeeping.

Only Vuja's retained pane and execution captures opt in, including fictional
pane scenarios in preview. Primary prompt/backdrop canvases and the preview's
final-screen decoder retain upstream canvas resize behavior.
The retained SDK owns soft wraps, width/height changes, hidden tails, active and
saved cursor positions, pending wrap, erasure and scrolling. Vuja no longer
injects post-resize cursor controls into those opted-in models. Search, copy,
export and new checkpoints use their full semantic text; only drawing wraps or
clips it. There is no application-owned terminal replay model.

Retention counts logical lines rather than geometry-created rows. An independent
1 MiB text/link/placeholder budget bounds long unbroken lines as well; UV cell
objects, visible rows and temporary layout allocations are additional memory,
not a 1 MiB heap/RSS guarantee. Actual eviction is disclosed separately from
window resizing. Existing checkpoints retain their original stored boundaries.

The SDK tests belong to `./internal/vt` and are included in root-module package
selection. Source/model/race and benchmark evidence must remain separate from
the Brian-run terminal matrix before installation or release.

## Current boundary

- Vuja's `internal/vt` package, derived from `github.com/charmbracelet/x/vt`,
  provides the shadow terminal emulator.
- `github.com/charmbracelet/ultraviolet` provides its backing screen buffer.
- Vuja sanitizes terminal control sequences before they reach the model,
  normalizes scroll margins after resize, bounds retained terminal state, and
  recovers disposable visual models without terminating the managed shell or
  durable history session.

Historical upstream reports for the same stale-boundary class include:

- [x/vt: clamp stale scroll margins to screen bounds](https://github.com/charmbracelet/x/pull/908)
- [ultraviolet: clamp stale line areas to buffer bounds](https://github.com/charmbracelet/ultraviolet/pull/135)

Until equivalent fixes are released and adopted, Vuja's defensive boundary is
the supported mitigation. A dependency bump must confirm whether those fixes
are present; it must not remove Vuja's recovery boundary merely because an
upstream change appears related.

## Update procedure

Every terminal dependency update requires:

1. A dedicated change with the old and new revisions recorded.
2. Review of upstream changes between those revisions.
3. The complete terminal compositor, resize, scroll-region, alternate-screen,
   Unicode, multiline-paste, and crash-recovery regression suites.
4. Race and long-session stress coverage.
5. The Ghostty and iTerm2 real-terminal matrix from `RELEASE.md`.
6. Confirmation that a visual-model failure degrades or recovers the display
   without terminating command execution or losing history.

If upstream cannot provide a reviewed revision that satisfies these gates,
Vuja keeps the known-good pins and its local containment. A maintained fork is
the fallback only when a required correctness or security fix cannot be
carried safely at the Vuja boundary.

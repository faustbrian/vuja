# Terminal dependency policy

Vuja treats its terminal emulator dependencies as a release-critical boundary.
They are intentionally pinned to exact pseudo-versions in `go.mod`; automated
dependency updates must not move them without the terminal regression and
real-terminal gates in `RELEASE.md`.

## Current boundary

- `github.com/charmbracelet/x/vt` provides the shadow terminal emulator.
- `github.com/charmbracelet/ultraviolet` provides its backing screen buffer.
- Vuja sanitizes terminal control sequences before they reach the model,
  normalizes scroll margins after resize, bounds retained terminal state, and
  recovers disposable visual models without terminating the managed shell or
  durable history session.

The upstream projects have open fixes for the same stale-boundary class:

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

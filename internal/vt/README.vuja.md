# Vuja's retained-output patch

This package is copied from `github.com/charmbracelet/x/vt v0.1.0`, upstream
revision `43427fdbad12fde220b737fc36303291a5ce4602` (the `vt/v0.1.0` tag in
`https://github.com/charmbracelet/x`). The original MIT license and source
provenance are retained. Vuja owns these patched bytes as `internal/vt` in its
root module, with shared dependency selection in the root `go.mod`. There is
no nested module, replacement directive or published fork. The upstream module
checksum does not authenticate the local modifications; the Vuja Git revision
identifies the tracked source.

Local changes add opt-in primary-screen reflow and a defensive logical-line
projection, eviction disclosure and a cell-anchor mapping resize operation.
Existing terminal interfaces and mutable cell/scrollback accessors retain their
signatures and aliasing behavior. Safe scrollback lookup serializes the alias
bookkeeping. Non-opted-in emulator instances keep upstream canvas resize
behavior for positive dimensions; resize dimensions are clamped to at least
one. Application canvases are not a transcript.

The SDK remains the only parser and terminal-state owner. Resize, cell edits,
erasure and scrolling update that state; Vuja does not reconstruct it by
replaying an application-owned transcript. Search/copy/export/checkpoints must
consume the same semantic projection while drawing clips independently.

Completed logical lines use exact-capacity cell storage. Reflow borrows its
newly owned logical projection for temporary layouts and copies only the
visible canvas into independently mutable padded rows; scrollback fragments
are reconstructed with one allocation per logical line. These reduce cell
capacity and copy costs without making the text-byte budget a heap limit.

Run this package's tests with `go test ./internal/vt`; root-module package
selection includes them. Agent runs
must use disposable caches and must never build or launch Vuja. Live terminal
acceptance remains separate from SDK/model tests.

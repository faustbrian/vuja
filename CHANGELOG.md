# Changelog

All notable changes to Vuja are recorded here. Vuja follows Semantic
Versioning after `v1.0.0`.

## 2.0.0

### Added

- Full-width chatbox surfaces and a pinned, scrollable output pane with search,
  selection, execution-local copy/export, and a return-to-latest control.
- Compact interactive command blocks with independent scrolling, Copy/Save
  actions, failure and incomplete-output states, and flush dim dividers on
  terminals supporting the required graphics protocol.
- Codex and Claude Code account status, retained exit/reconnect instructions,
  and clickable resume actions admitted by the actual Zsh editor.
- Running-output checkpoints with pane identity, stale-writer fencing,
  credential-free resume-link restoration, bounded retention and explicit
  incomplete-output disclosure.
- Recording pause/resume, selective history forgetting, age limits, and
  portable native history archives. External history tools remain optional.
- Privacy-safe diagnostics and actionable session/provider health for history,
  workspace metadata, SSH discovery, recipes, processes and external providers.
- Configuration presets, real layout previews, guided setup and suggestion
  curation controls; SSH discovery follows supported local include rules.

### Changed

- Managed drag selections copy on release by default. Set
  `ui.chatbox.copy-on-select = false` for explicit Ctrl+C copying, or choose
  native selection mode to use terminal-owned selection.
- Go source installation and imports use `github.com/faustbrian/vuja/v2`.
  Release archives, executable name and configuration/history locations are
  unchanged. Source builds require Go 1.26.10 or newer.
- Retained logical text survives drawing-width changes without treating soft
  wraps as newlines. Complete owned rows avoid a redundant resize copy,
  reducing temporary allocation by about 18% in the documented large fixture;
  this is not a process-RAM or live-resize latency guarantee.

### Fixed

- Long-session terminal lifecycle, native-application handoff, quiet-command
  visibility and running-state animation.
- Startup-size reconciliation and concurrent history initialization during
  pane restoration, without assuming a later resize notification.
- Delayed history recovery, bounded interactive lock waits, native Zsh/Fish
  history policy and complete accepted multiline input.
- Clear-command handling, retained output across restarts, frozen reading
  anchors, Unicode resize semantics and independent pane checkpoint recovery.
- Stable suggestion identity during asynchronous refreshes, deterministic
  ordering and accurate acquisition-failure reporting.
- Release quality gates, supported-shell execution, patched Go qualification,
  nightly version eligibility and native artifact version injection. Archives
  include license notices alongside checksums and provenance.

## 1.0.0

### Added

- Vuja-owned durable command history with optional Atuin and native-shell
  adapters.
- Bottom-positioned chatbox, historical execution snapshots, configurable
  status segments, finite provider detection, and responsive density modes.
- Zsh, Bash, and Fish function, alias, history, and managed-prompt integration.
- Crash recovery, bounded terminal state, resize hardening, and local recovery
  journaling for history submissions.
- Stable, release-candidate, and nightly installer and updater channels with
  semantic prerelease filtering.
- Checksum-verified multi-platform release archives and build-provenance
  attestations.

### Changed

- Atuin, zoxide, Starship, and shell autosuggestion plugins are no longer
  required for complete Vuja history and suggestion behavior.
- Uninstall preserves configuration and durable history by default; explicit
  `--purge` removes retained user data.

### Security

- History, logs, configuration, SQLite sidecars, action sockets, and recovery
  data use owner-only permissions.

## Release history

[`v1.0.0`](https://github.com/faustbrian/vuja/releases/tag/v1.0.0) was published
on 2026-09-04. Its published stable and candidate tags
remain immutable; the v2 milestone does not replace that release history.

# Changelog

All notable changes to Vuja are recorded here. Vuja follows Semantic
Versioning after `v1.0.0`.

## Unreleased

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

No stable release has been published yet. The first stable entry will be
`v1.0.0` after the release-candidate checklist passes.

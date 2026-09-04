# Support policy

## Supported environments

Vuja v1 supports these primary environments:

| Platform | Architectures | Shells | Primary terminals |
| --- | --- | --- | --- |
| macOS | arm64, amd64 | Zsh, Bash, Fish | Ghostty, iTerm2 |
| Linux | arm64, amd64 | Zsh, Bash, Fish | ANSI-compatible terminals |

Windows and shells other than Zsh, Bash, and Fish are not supported in v1.
Terminal-specific behavior outside Ghostty and iTerm2 is supported on a
best-effort basis unless it is represented in the release compatibility
matrix.

## Compatibility guarantees

Within the v1 major series:

- documented configuration remains compatible or is migrated automatically;
- Vuja-owned history is migrated transactionally and is not discarded during
  an upgrade;
- documented CLI commands and exit behavior remain backward compatible;
- stable update channels never install prerelease or nightly builds; and
- release archives continue to be published for the four platform and
  architecture combinations above.

An unavoidable incompatible change requires a new major version and migration
guidance. Experimental or undocumented internals are not compatibility
contracts.

## Maintained versions

The latest stable minor release receives fixes. Security fixes may be
backported when the affected stable version remains widely deployed. Nightly
and release-candidate builds are testing channels and do not receive backports.

## Reporting problems

Use GitHub issues for reproducible bugs. Use private GitHub Security Advisories
for vulnerabilities. Before attaching a crash report or debug log, remove
commands, paths, hostnames, session identifiers, environment values, and other
private terminal content.

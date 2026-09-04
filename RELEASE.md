# Release process

This checklist is mandatory for `v1.0.0` and subsequent stable releases. A
green source test suite is necessary but does not substitute for real-terminal
verification.

## 1. Source and repository gates

- [ ] The release commit is on `main`, the worktree is clean, and the intended
      tag points to that exact commit.
- [ ] Release Quality, Linux compatibility, macOS compatibility, CodeQL, and
      spelling checks pass without skipped required steps.
- [ ] `main` is protected against direct unverified changes and force pushes.
- [ ] The final diff has no unresolved review findings.
- [ ] The protected `release` environment approval is recorded before any
      candidate or stable assets are published.
- [ ] `CHANGELOG.md`, `SUPPORT.md`, installation instructions, and migration
      guidance describe the release behavior.
- [ ] Terminal dependencies are pinned to reviewed revisions and their resize,
      scroll-region, and recovery regressions pass.

## 2. Performance and durability gates

Run and retain the output for the release commit:

```bash
go test -run '^$' -bench 'BenchmarkCanonicalHistory' -benchmem ./integration ./root
go test -run '^$' -bench 'Benchmark(ScoreCandidates|FileGeneratorPrefix|Search.*RichHistory.*10K)$' -benchmem -count=5 ./internal/scoring ./spec ./integration
go test -run '^$' -bench 'Benchmark(Chatbox|MergeResults|Prompt|Repository|Status|SuggestionPipeline|System)' -benchmem -count=5 ./root
```

- [ ] Histories with at least 100,000 events and 10,000 distinct commands stay
      within the documented lookup and publication budgets.
- [ ] Thirty distinct `ssh forge@host` commands remain eligible after restart
      without Atuin or native shell history.
- [ ] A submitted long-running command is durable before completion.
- [ ] Forced termination recovers unfinished events without losing completed
      history.
- [ ] Multi-day sessions keep bounded memory and durable history through
      repeated resizes.

For the 100,000-event fixture on a supported release machine, use these
release budgets. Record the machine with the benchmark output; shared CI does
not enforce wall-clock thresholds because runner load is variable.

| Path | Budget |
| --- | ---: |
| Cold in-memory publication | under 2s/op |
| Cold persistent-store load and publication | under 2s/op |
| Warm history prefix lookup | under 1ms/op |
| Incremental `s` to `ss` to `ssh` lookup | under 5ms/op |
| Single submitted-event publication | under 100µs/op |

## 3. Real-terminal compatibility matrix

Exercise both a fresh configuration and an upgraded existing configuration.
Record the Vuja version, OS version, shell version, terminal version, result,
and any issue link.

| OS | Architecture | Terminal | Shell | Fresh | Upgrade | Long-session/resize |
| --- | --- | --- | --- | --- | --- | --- |
| macOS | arm64 | Ghostty | Zsh | [ ] | [ ] | [ ] |
| macOS | arm64 | iTerm2 | Zsh | [ ] | [ ] | [ ] |
| macOS | arm64 | Ghostty or iTerm2 | Bash | [ ] | [ ] | [ ] |
| macOS | arm64 | Ghostty or iTerm2 | Fish | [ ] | [ ] | [ ] |
| macOS | amd64 | Ghostty or iTerm2 | Zsh | [ ] | [ ] | [ ] |
| Linux | amd64 | ANSI terminal | Zsh | [ ] | [ ] | [ ] |
| Linux | amd64 | ANSI terminal | Bash | [ ] | [ ] | [ ] |
| Linux | amd64 | ANSI terminal | Fish | [ ] | [ ] | [ ] |
| Linux | arm64 | ANSI terminal | Zsh, Bash, or Fish | [ ] | [ ] | [ ] |

Each run covers multiline paste, large output, alternate-screen applications,
SSH, background output, prompt suggestions, Up/Down history, Ctrl+R, terminal
resize, split creation/removal, window movement, fullscreen transitions, clean
shutdown, forced termination, and restart recovery.

## 4. Distribution gates

- [ ] Publish `vX.Y.Z-rc.N` as a GitHub prerelease from the intended commit.
- [ ] Confirm stable installers and updaters ignore the release candidate.
- [ ] Confirm the explicit release-candidate channel selects it.
- [ ] Verify all four archives against `SHA256SUMS`.
- [ ] Verify GitHub build-provenance attestations for every archive.
- [ ] Fresh-install every archive in its matching environment.
- [ ] Upgrade from the newest stable release and newest nightly release.
- [ ] Exercise configuration and history migrations from every supported schema.
- [ ] Verify uninstall and rollback instructions without losing user history.

## 5. Candidate promotion

Dogfood the release candidate for at least three days, including one long-lived
interactive session. Any code change restarts the candidate period and all
affected gates.

Promote the exact accepted commit:

```bash
git tag -s vX.Y.Z <accepted-commit>
git push origin vX.Y.Z
```

After publication:

- [ ] GitHub marks the stable release as non-prerelease and `/releases/latest`
      resolves to it.
- [ ] Release notes match `CHANGELOG.md`.
- [ ] Installer and `vuja update` install the stable version.
- [ ] Checksums and attestations verify from a clean machine.
- [ ] A newly installed terminal session passes the primary smoke journey.

If a gate fails, stop promotion, preserve the failed candidate for diagnosis,
and publish a new candidate after the fix. Never move or overwrite a published
release tag.

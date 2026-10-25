# Codex Project Rules

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and
"OPTIONAL" in this document are to be interpreted as described in BCP 14
[RFC2119] [RFC8174] when, and only when, they appear in all capitals, as shown
here.

## Hard Block: Never Build Vuja Or Disrupt Live Sessions

For the v2.0.0 stabilization goal, the Hosted CI Exception below overrides
this section only for isolated GitHub-hosted jobs. All local build,
installation, reload and existing-session prohibitions remain in force.

Codex and every agent, subagent, automation, tool, and subprocess acting on its
behalf MUST NOT build or install Vuja, start a watchdog, reload or replace an
existing Vuja process, or otherwise interfere with an existing session. A
managed shell MAY be started only under Isolated Installed-Binary Verification
below. Agents MUST NOT run a test that can crash, signal, replace, reload,
or otherwise interfere with an existing Vuja session. This prohibition applies
even when those actions would normally be part of implementation, debugging,
testing, verification, review, installation, or completion. Read-only operations
using an already installed Vuja binary are allowed under the rules below.

The following actions are explicitly forbidden:

- executing Vuja outside a verified read-only subcommand or the isolated
  installed-binary verification rules, starting a watchdog, or executing a
  newly compiled Vuja binary;
- building or installing the Vuja executable, including with `go build`,
  `go install`, release tooling, or a wrapper such as `just build` or
  `just install`;
- running broad commands such as `go test ./...` or `just test` when their
  selected packages include a test that builds or starts Vuja;
- running `tests/e2e` or any other test, benchmark, analyzer, or script that
  builds, reloads, installs, signals an existing session, or otherwise
  interferes with existing Vuja; managed test sessions MUST satisfy the
  isolated installed-binary verification rules;
- sourcing generated Vuja shell integration or starting a shell, terminal,
  PTY, tmux session, or subprocess in a way that can auto-start Vuja outside
  the isolated installed-binary verification rules;
- sending signals to an inherited `VUJA_PID` or any process not created and
  verified as owned by the current isolated test;
- invoking another agent, tool, IDE task, CI command, or wrapper to perform an
  action forbidden above.

This is a hard safety boundary, not a preference. Brian MUST perform Vuja
builds, installations and reloads himself. Agents MAY perform read-only runtime
inspection, verified read-only CLI checks and isolated installed-binary
verification without asking Brian to run them. The repository policy itself
must be deliberately changed before Codex may run any other forbidden command.

## Hosted CI Exception For v2.0.0 Stabilization

Agents MAY trigger and inspect the repository's existing GitHub Actions
checks, isolated test builds, cross-platform artifact builds and release
packaging for the authorized v2.0.0 stabilization goal. These jobs MUST run
on disposable GitHub-hosted runners, MUST NOT access or control existing
user terminal sessions, and MUST NOT deploy or install Vuja on user machines.
Publishing release candidates and the stable release remains subject to
the authorized release scope and every gate in `RELEASE.md`; this exception
MUST NOT authorize replacing published tags or unrelated tag deletion.

Agents MUST NOT use hosted CI to bypass a failed release gate or to perform
any otherwise unauthorized runtime mutation. Local builds, installations,
newly compiled Vuja execution, watchdogs, reloads and interference with live
sessions remain forbidden.

## Isolated Installed-Binary Verification

Agents MAY exercise the already installed Vuja binary in a task-owned PTY,
including its generated shell integration, without rebuilding or installing it.
This permission MUST NOT be used to control an existing terminal or session.

- All writes MUST remain in disposable task-owned storage. Set private `HOME`,
  `ZDOTDIR`, `TMPDIR` and XDG config/data/cache/runtime directories; use a
  disposable working directory and minimal fixture shell configuration.
  Do not source the user's dotfiles or use their history or credentials.
- Clear inherited `VUJA_*`, shell-startup overrides, Codex home, terminal pane
  identities and tmux variables. Then set `VUJA_IS_CHILD=true` only for the
  task-owned process so it cannot start the watchdog. Any fixture pane identity
  MUST be newly generated, never copied from a real pane.
- Disable update checks, external history integrations and network-dependent
  providers in the fixture configuration. Do not run setup, installation,
  update or reload commands. An installed binary MUST NOT be replaced.
- Keystrokes, resizing, normal exit and intentional interruption MAY target
  only the captured, verified task-owned PTY and processes. Prefer normal exit;
  cleanup signals MUST NOT use inherited PIDs, broad process-name matching or
  unrelated process groups.
- Close and reap owned processes before removing their disposable storage,
  including on failure. Capture bounded fixture-only evidence and remove all
  temporary artifacts and caches when no longer needed.
- PTY evidence MUST remain distinct from actual iTerm2/Ghostty mouse selection,
  clipboard, window restoration and multi-day-use evidence. Do not claim those
  platform behaviors from a PTY test alone.

## Allowed Agent Actions

Agents MAY:

- inspect files with non-executing tools such as `rg`, `sed`, and `git diff`;
- inspect repository state with read-only Git commands;
- inspect existing history, configuration, diagnostics, and process metadata
  read-only, without exposing credentials or unrelated command/output text;
- run verified read-only subcommands of an already installed Vuja binary, such
  as `vuja debug diagnostics`. Before invocation, inspect dispatch, helpers, and
  transitive setup to establish that the command cannot enter the managed shell
  or watchdog, build/install/reload Vuja, signal a session, modify history or
  configuration, replay recovery records, or start background workers. A
  read-like command name or preview flag alone does not establish this;
- run isolated installed-binary verification under the section above;
- create or edit files with `apply_patch`;
- run `gofmt` on explicitly named Go files because it formats source without
  compiling or executing project code;
- run package-scoped Go tests, race tests, benchmarks, linters, analyzers, and
  other repository checks after inspecting their helpers and transitive setup
  to establish that they do not build or interfere with existing Vuja sessions;
  tests exercising installed Vuja MUST satisfy the isolated verification rules;
- run repository tooling that does not build Vuja or interfere with live
  sessions;
- run static text checks such as `git diff --check` that do not invoke hooks or
  project executables.

Before running an allowed test or tool, agents MUST remove inherited `VUJA_PID`,
`VUJA_FD`, `VUJA_HISTORY_ACK_FD`, `VUJA_IS_CHILD`, and
`VUJA_MANAGED_PROMPT` from its subprocess environment. Every allowed Go test,
race test, benchmark, or analyzer MUST use a task-owned disposable `GOCACHE`
that is removed after the command, including on failure.

If inspection cannot establish that a Vuja invocation is read-only or satisfies
the isolated verification rules, or that another test/tool is isolated from
existing-session interference, the agent MUST treat it as forbidden. Isolated
tests MAY write task-owned disposable fixtures. Agents MUST perform available
safe verification themselves. A genuinely forbidden boundary MUST be reported
as not run rather than represented as a passing result.

[RFC2119]: https://www.rfc-editor.org/rfc/rfc2119
[RFC8174]: https://www.rfc-editor.org/rfc/rfc8174

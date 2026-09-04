# Codex Project Rules

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and
"OPTIONAL" in this document are to be interpreted as described in BCP 14
[RFC2119] [RFC8174] when, and only when, they appear in all capitals, as shown
here.

## Hard Block: Never Build Or Execute Vuja

Codex and every agent, subagent, automation, tool, and subprocess acting on its
behalf MUST NOT build, execute, launch, reload, install, exercise, or indirectly
start Vuja. It also MUST NOT run a test that can crash, signal, replace, reload,
or otherwise interfere with a live Vuja session. This prohibition applies even
when execution would normally be part of implementation, debugging, testing,
verification, review, installation, or completion.

The following actions are explicitly forbidden:

- executing any `vuja` binary, including `./vuja`, `~/.local/bin/vuja`, a
  temporary copy, a test fixture, or a newly compiled binary;
- building or installing the Vuja executable, including with `go build`,
  `go install`, release tooling, or a wrapper such as `just build` or
  `just install`;
- running broad commands such as `go test ./...` or `just test` when their
  selected packages include a test that builds or starts Vuja;
- running `tests/e2e` or any other test, benchmark, analyzer, or script that
  builds, launches, reloads, installs, signals, or exercises Vuja;
- sourcing generated Vuja shell integration or starting a shell, terminal,
  PTY, tmux session, or subprocess in a way that can auto-start Vuja;
- sending signals to `VUJA_PID` or another Vuja process;
- invoking another agent, tool, IDE task, CI command, or wrapper to perform an
  action forbidden above.

This is a hard safety boundary, not a preference. Chat instructions MUST NOT be
treated as sufficient authorization to bypass it. Brian MUST perform Vuja
builds, installations, reloads, end-to-end tests, and runtime verification
himself. The repository policy itself must be deliberately changed before Codex
may run any forbidden command.

## Allowed Agent Actions

Agents MAY:

- inspect files with non-executing tools such as `rg`, `sed`, and `git diff`;
- inspect repository state with read-only Git commands;
- create or edit files with `apply_patch`;
- run `gofmt` on explicitly named Go files because it formats source without
  compiling or executing project code;
- run package-scoped Go tests, race tests, benchmarks, linters, analyzers, and
  other repository checks after inspecting their helpers and transitive setup
  to establish that they do not build, start, signal, reload, or exercise Vuja;
- run repository tooling that does not build or execute Vuja;
- run static text checks such as `git diff --check` that do not invoke hooks or
  project executables.

Before running an allowed test or tool, agents MUST remove `VUJA_PID`,
`VUJA_FD`, `VUJA_HISTORY_ACK_FD`, `VUJA_IS_CHILD`, and
`VUJA_MANAGED_PROMPT` from its subprocess environment. Every allowed Go test,
race test, benchmark, or analyzer MUST use a task-owned disposable `GOCACHE`
that is removed after the command, including on failure.

If inspection cannot establish that a command is isolated from Vuja, the agent
MUST treat it as forbidden. Agents MUST stop only at the Vuja build/runtime
verification boundary, give Brian the exact remaining command, and report that
boundary as not run.

[RFC2119]: https://www.rfc-editor.org/rfc/rfc2119
[RFC8174]: https://www.rfc-editor.org/rfc/rfc8174

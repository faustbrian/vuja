# Codex Project Rules

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and
"OPTIONAL" in this document are to be interpreted as described in BCP 14
[RFC2119] [RFC8174] when, and only when, they appear in all capitals, as shown
here.

## Hard Block: Never Execute Vuja

Codex and every agent, subagent, automation, tool, and subprocess acting on its
behalf MUST NOT execute Vuja or any project command that can build, launch,
reload, install, exercise, or indirectly start Vuja. This prohibition applies
even when execution would normally be part of implementation, debugging,
testing, verification, review, installation, or completion.

The following actions are explicitly forbidden:

- executing any `vuja` binary, including `./vuja`, `~/.local/bin/vuja`, a
  temporary copy, a test fixture, or a newly compiled binary;
- running `go run`, `go build`, `go install`, or `go test` anywhere in this
  repository;
- running any Just recipe or script that builds, tests, analyzes, benchmarks,
  installs, copies, starts, reloads, debugs, or exercises Vuja, including
  `just build`, `just run`, `just reload`, `just test`, `just bench`,
  `just analyze`, and `just install`;
- running linters, analyzers, benchmarks, race tests, integration tests, PTY
  tests, shell-integration tests, or end-to-end tests against this repository;
- sourcing generated Vuja shell integration or starting a shell, terminal,
  PTY, tmux session, or subprocess in a way that can auto-start Vuja;
- sending signals to `VUJA_PID` or another Vuja process;
- invoking another agent, tool, IDE task, CI command, or wrapper to perform an
  action forbidden above.

This is a hard safety boundary, not a preference. Chat instructions MUST NOT be
treated as sufficient authorization to bypass it. Brian MUST perform all
builds, tests, checks, installations, reloads, and runtime verification himself.
The repository policy itself must be deliberately changed before Codex may run
any forbidden command.

## Allowed Agent Actions

Agents MAY:

- inspect files with non-executing tools such as `rg`, `sed`, and `git diff`;
- inspect repository state with read-only Git commands;
- create or edit files with `apply_patch`;
- run `gofmt` on explicitly named Go files because it formats source without
  compiling or executing project code;
- run static text checks such as `git diff --check` that do not invoke hooks or
  project executables.

Agents MUST stop at the verification boundary and give Brian the exact commands
he needs to run. Agents MUST report those checks as not run and MUST NOT claim
the repository is verified, ready, or complete from static inspection alone.

[RFC2119]: https://www.rfc-editor.org/rfc/rfc2119
[RFC8174]: https://www.rfc-editor.org/rfc/rfc8174

# Live terminal acceptance

This is Brian-run acceptance, not an agent-executable test script. Model/race
tests cannot prove native text selection, clipboard permissions, terminal
restoration, or real window/pane behavior. Unchecked cases remain unverified.

Use disposable tabs and harmless fixture output. Do not force-stop a working
Codex chat or run history deletion against commands you need to retain. Never
start a nested Vuja wrapper merely to change shells.

## Record the environment

Record the source commit, installed Vuja version, macOS version, terminal
version, shell/version, and relevant configuration. Run the core cases in each
available pairing of iTerm2/Ghostty with Zsh/Bash/Fish. Mark missing pairings
as not run, not passed. Use the terminal's ordinary profile/shell startup.

Keep the default `output-mouse = "select"` and `surface-width = "full-width"`
unless the case explicitly changes them. Record `output-viewport`,
`output-lines`, checkpoint mode/interval/retention and density. Do not include
credentials, real command output, identifying paths, or account email in a
shared acceptance report.

## Core cases

| Case | Action | Pass condition |
| --- | --- | --- |
| Ordinary selection | Drag over output with no modifier; copy normally. Also select across wrapped lines. | Drag creates native selection immediately. Copied text is exact; no mouse-reporting warning or extra selection key is required. |
| Multiline paste | Paste the fixture below without pressing Enter, edit its middle line, then press Enter once. | Nothing executes during paste. All lines remain inside the growing chatbox with one consistent background. One Enter submits the pasted input. |
| Running command | Run the quiet fixture below; resize and move the window while it sleeps. | Output, title and status stay visible. A smooth running indicator remains visible; completion replaces it with the outcome. |
| Frozen reading | Generate the large fixture, browse upward, then generate more output. | The input remains fixed; incoming output does not move the reading anchor. Down/End/Esc returns to the latest output. |
| Search/navigation | Search for `fixture-row-00100`; use next/previous match and previous/next execution navigation. | Match position is accurate, boundaries are deterministic, input is unchanged, and no command is submitted. |
| Copy/export | Copy one selected fixture execution; export it and the retained pane. Narrow the window and repeat. | Plain text has no neighboring execution or unsafe controls. Retained text is preserved, or any loss is explicitly disclosed; no silent clipping. |
| Async suggestions | Select a non-first suggestion while results refresh; press Enter. Test unselected typed input separately. | Selected command identity does not change during reordering. Enter runs only typed input or an explicitly selected command, never incidental ghost text. |
| Empty-prompt history | Run distinct harmless commands and a repeated command, then open history with Up. | Most recent execution starts at the bottom. Repeated Up moves toward older executions in chronological order; duplicates remain distinct. |
| Native application | Open a normal terminal application, such as `less README.md`; exercise its input and quit. Repeat with an existing Codex session. | The application owns its input/screen; Vuja shortcuts do not hijack it. Returning restores a usable prompt and retained output without blackouts. |
| Pane isolation | Use two tabs/panes with distinct fixture sentinels. Move, resize, split and rejoin them repeatedly. | Output, recovery and selected input never cross between panes. Title/status geometry follows the actual pane size. |

Paste fixture (paste only first; do not press Enter until checking its layout):

```sh
printf 'paste-one\n'
printf 'Unicode: café 日本語 👩🏽‍💻\n'
printf 'paste-three\n'
```

Quiet running fixture:

```sh
printf 'running-start\n'; sleep 15; printf 'running-end\n'
```

Bounded output fixture, using the system's `awk`:

```sh
awk 'BEGIN { for (i = 1; i <= 10000; i++) printf "fixture-row-%05d café 日本語\n", i }'
```

Output-tool key bindings and retention disclosures are documented in the
README. Test both narrow and wide panes and both light and dark palettes. Also
test content-width mode and explicit mouse-navigation mode separately; do not
credit modifier-based selection there as proof of the default native-drag case.

## Recovery and privacy cases

Use a disposable pane with a stable terminal session ID. Change checkpoint
settings before opening the new session; do not assume hot reload.

1. With `output-checkpoints = "running"`, emit a unique harmless sentinel,
   then leave a command running beyond the configured checkpoint interval.
   Force-stop only that disposable pane's Vuja process using a verified PID,
   and restore/reopen the same terminal pane. The output must return with
   `Output restored; process not resumed`. The old process must not resume.
2. Repeat with `completed`: unfinished new output must not be claimed durable.
   Repeat with `off`: old output must not restore, and an older session's queued
   writer must not defeat disablement. Previously saved files are not erased
   merely by selecting `off`.
3. Preview checkpoint clearing. Verify no output disappears from disk until
   confirmation. Restart all pre-update Vuja sessions before supplying the
   required `--old-sessions-restarted` acknowledgement; an updated CLI cannot
   fix a still-running old writer's missing-file behavior. Then verify the
   selected scope stays cleared after a stale updated writer is released.
   Use `--all` only for an intentionally disposable
   checkpoint set: it includes recognized interrupted-write files from all panes.
   History and explicit saved exports must remain unchanged.
4. Exercise age/pane retention at a small test limit. Pane moves must preserve
   identity; separate panes must not restore each other's output. Unsupported
   or ambiguous identities, including inherited tmux identity, must not guess.
5. With harmless history fixtures only, preview/confirm forgetting one
   execution and a matched command. Check another session and a restart;
   delayed completion, journal recovery and reimport must not resurrect them.
   Paused recording must be clearly visible and must not create new history or
   derived learning. Output checkpoints follow their own policy.

Safe checkpoint inventory and preview commands (confirmation is printed only
after inspecting the actual selection):

```sh
vuja output checkpoints list
vuja output checkpoints clear
```

Do not publish the captured files themselves as evidence. Record only the
fixture sentinel's presence/absence and the expected scope of deletion.

## Sustained use and result record

Repeat resize/movement while a command is quiet, emitting output, displaying an
alternate screen, and while reading old output. Include a real long-lived
session; record its uptime and workload rather than calling a short trial
multi-day evidence. Observe memory at increasing fixture sizes after warm-up.
Do not infer a leak from one RSS sample or a latency guarantee from one run.

For each case record: terminal/shell versions, source/installed version,
configuration, pass/fail/not-run, decisive harmless screenshot or transcript,
and any crash report. Keep source/model results, locally installed behavior,
and whole live-matrix completion separate. A passed subset does not close the
unrun terminal/shell or forced-termination cases.

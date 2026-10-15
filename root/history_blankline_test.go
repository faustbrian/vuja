package root

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/internal/scoring"
)

// Exercise the actual generated shell sender and shared durable admission,
// without building or starting a Vuja executable or reading user dotfiles.
func TestHistoryBlankLinesPreserveShellAdmissionAndExactDurability(t *testing.T) {
	for _, shellName := range []string{"zsh", "fish"} {
		t.Run(shellName, func(t *testing.T) {
			shellPath, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s not available", shellName)
			}
			for _, test := range []struct {
				name, raw, exclusion string
				paused, want         bool
			}{
				{name: "ordinary", raw: "printf public", want: true},
				{name: "empty LF lines", raw: "\n\nprintf 'café 日本語\\n'\nprintf second  ", want: true},
				{name: "empty CRLF lines", raw: "\r\n\r\nprintf public", want: true},
				{name: "empty CR lines", raw: "\r\rprintf public", want: true},
				{name: "literal leading space", raw: " printf private"},
				{name: "space after empty lines", raw: "\n printf private"},
				{name: "tab after empty lines", raw: "\r\n\tprintf private"},
				{name: "space before empty lines", raw: " \nprintf private"},
				{name: "nonempty whitespace line", raw: "\n \nprintf private"},
				{name: "Unicode space after empty lines", raw: "\n\u00a0printf private"},
				{name: "empty only", raw: "\r\n\n"},
				{name: "native hook exclusion", raw: "\nprintf private", exclusion: "hook"},
				{name: "native hook status two", raw: "\nprintf private", exclusion: "hook2"},
				{name: "native private policy", raw: "\nprintf private", exclusion: "private"},
				{name: "paused", raw: "\nprintf public", paused: true},
				{name: "sensitive", raw: "\ncurl --token private"},
				{name: "forgetting", raw: "\nvuja history forget --command private"},
			} {
				t.Run(test.name, func(t *testing.T) {
					home := t.TempDir()
					hook := filepath.Join(home, "hook")
					if err := os.WriteFile(hook, []byte(shellInitScript(shellName, "/unused/vuja")), 0o600); err != nil {
						t.Fatal(err)
					}
					var args []string
					if shellName == "zsh" {
						args = []string{"-f", "-c", `
if [[ $3 == hook ]]; then zshaddhistory() { return 1 }; fi
if [[ $3 == hook2 ]]; then zshaddhistory() { return 2 }; fi
if [[ $3 == private ]]; then HISTORY_IGNORE='*private*'; fi
source "$1"
zshaddhistory "$2"
_vuja_preexec "$2"
`, "fixture", hook, test.raw, test.exclusion}
					} else {
						args = []string{"--no-config", "-c", `
# Fish --no-config enables private mode. Exercise the ordinary history hook
# explicitly; private-mode exclusion has separate coverage.
set -e fish_private_mode
if test "$argv[3]" = hook
    function fish_should_add_to_history; return 1; end
end
if test "$argv[3]" = hook2
    function fish_should_add_to_history; return 2; end
end
if test "$argv[3]" = private
    set -g fish_private_mode 1
end
source "$argv[1]"
fish_should_add_to_history "$argv[2]"
emit fish_preexec "$argv[2]"
`, hook, test.raw, test.exclusion}
					}
					command := exec.CommandContext(t.Context(), shellPath, args...)
					command.Env = []string{
						"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ZDOTDIR=" + home,
						"XDG_CONFIG_HOME=" + home, "XDG_DATA_HOME=" + home, "XDG_CACHE_HOME=" + home,
						"TMPDIR=" + home, "LANG=en_US.UTF-8", "VUJA_PID=1", "VUJA_FD=1",
					}
					output, err := command.Output()
					if err != nil {
						t.Fatalf("private %s hook: %v", shellName, err)
					}
					reader := bufio.NewReader(bytes.NewReader(output))
					frame, oversized, err := readShellMessage(reader, shellMessageByteLimit)
					if err != nil || oversized {
						t.Fatalf("invalid start frame: %v, oversized=%v", err, oversized)
					}
					matched, ignored := parseCommandStartMessage(string(frame))
					if !matched {
						t.Fatalf("missing shell start frame: %q", frame)
					}
					store, err := scoring.NewFrecencyStore(filepath.Join(home, "history.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer store.Close()
					admission := historySubmissionAdmission{paused: test.paused}
					tracker := newCommandExecutionTracker(nil, "blank-lines", "fixture")
					entry, admitted := admission.startExecution(tracker, string(frame), test.raw, "/fixture", shellName, ignored)
					if admitted != test.want || entry.Command != test.raw {
						t.Fatalf("accepted multiline input lost or misclassified: admitted=%v want=%v ignored=%v raw=%q", admitted, test.want, ignored, entry.Command)
					}
					persisted, journaled := admission.persist(store, entry, ignored, nil)
					if persisted != test.want || journaled {
						t.Fatalf("durability disagreed with admission: persisted=%v journaled=%v", persisted, journaled)
					}
					if admitted {
						completed := tracker.Finish(0)
						if ok, err := store.CompleteHistoryEvent(t.Context(), scoringHistoryEvent(completed)); !ok || err != nil {
							t.Fatalf("completion: %v, %v", ok, err)
						}
						recordSuggestionFeedbackToStore(t.Context(), store, (suggestionFeedbackSubmission{}).finish(entry.Command), entry.Cwd, entry.ID)
					}
					events, err := store.QueryAllHistoryEvents(t.Context())
					if err != nil || len(events) != boolCount(test.want) {
						t.Fatalf("unexpected durable event count: %d, %v", len(events), err)
					}
					if test.want {
						if events[0].Command != test.raw || events[0].NormalizedCommand != strings.TrimSpace(test.raw) || events[0].State != "completed" {
							t.Fatalf("raw text or outcome changed: %+v", events[0])
						}
						feedback, err := store.QueryFeedback(t.Context(), entry.Cwd, "", "printf", 10)
						if err != nil || len(feedback) != 1 || feedback[0].Typed != 1 {
							t.Fatalf("accepted multiline feedback missing: %+v, %v", feedback, err)
						}
					}
				})
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

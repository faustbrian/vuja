package root

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportedShellsEmitOrderedTerminalMarkers(t *testing.T) {
	const marker = "marker-test-session"
	tests := []struct {
		shell   string
		args    []string
		command string
	}{
		{
			shell: "zsh",
			args:  []string{"-f", "-c"},
			command: `source "$1"
if [[ ${POWERLEVEL9K_INSTANT_PROMPT-} != off ]]; then
  print -rn -- 'unexpected instant prompt'
fi
late_prompt_setup() { PS1='late λ '; PS2='late > '; }
autoload -Uz add-zsh-hook
add-zsh-hook precmd late_prompt_setup
source "$1"
for hook in $precmd_functions; do
  "$hook"
done
PS2='> '
print -rnP -- "$PS1"
_vuja_preexec`,
		},
		{
			shell: "bash",
			args:  []string{"--noprofile", "--norc", "-c"},
			command: `source "$1"
starship_precmd() { PS1='late λ '; PS2='late > '; }
STARSHIP_PROMPT_COMMAND=$PROMPT_COMMAND
PROMPT_COMMAND=starship_precmd
source "$1"
eval "$PROMPT_COMMAND"
expanded=${PS1//\\[/}
expanded=${expanded//\\]/}
printf '%b' "$expanded"
_vuja_preexec`,
		},
		{
			shell: "fish",
			args:  []string{"--no-config", "-c"},
			command: `source "$argv[2]"
function fish_prompt
    printf 'late λ '
end
source "$argv[2]"
emit fish_prompt
fish_mode_prompt
fish_prompt
fish_right_prompt
emit fish_preexec
emit fish_postexec`,
		},
	}

	for _, test := range tests {
		t.Run(test.shell, func(t *testing.T) {
			path, err := exec.LookPath(test.shell)
			if err != nil {
				t.Skipf("%s is not installed", test.shell)
			}

			integrationPath := filepath.Join(t.TempDir(), "init."+test.shell)
			if err := os.WriteFile(integrationPath, []byte(shellInitScript(test.shell, "/unused/vuja")), 0600); err != nil {
				t.Fatal(err)
			}

			args := append(append([]string(nil), test.args...), test.command, "vuja-marker-test", integrationPath)
			command := exec.Command(path, args...)
			command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=2", "VUJA_MARKER="+marker)
			output, err := command.Output()
			if err != nil {
				t.Fatalf("%s integration failed: %v", test.shell, err)
			}

			assertOrderedMarkers(t, string(output), marker)
			if test.shell == "zsh" && strings.Contains(string(output), "unexpected instant prompt") {
				t.Fatalf("expected bottom mode to suppress the unmarked Powerlevel10k instant prompt, got %q", output)
			}
		})
	}
}

func assertOrderedMarkers(t *testing.T, output, marker string) {
	t.Helper()
	events := []string{"prompt-start", "prompt-end", "command-start"}
	previous := -1
	for _, event := range events {
		sequence := terminalMarker(marker, event)
		index := strings.Index(output, sequence)
		if index < 0 {
			t.Fatalf("expected %q marker in shell output %q", event, output)
		}
		if count := strings.Count(output, sequence); count != 1 {
			t.Fatalf("expected one %q marker, got %d in %q", event, count, output)
		}
		if index <= previous {
			t.Fatalf("expected ordered markers %v, got %q", events, output)
		}
		previous = index
	}
	if count := strings.Count(output, "\x1b]777;vuja;"+marker+";command-end:"); count != 1 {
		t.Fatalf("expected one command-end marker, got %d in %q", count, output)
	}
}

func TestZshInitReportsHistoryIgnoreWithoutSendingTheCommand(t *testing.T) {
	script := shellInitScript("zsh", "/unused/vuja")
	for _, expected := range []string{
		`[[ $1 == [[:space:]]* ]]`,
		`[[ -n ${HISTORY_IGNORE-} && $1 == ${~HISTORY_IGNORE} ]]`,
		`VUJA_CMD_START:IGNORE`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("expected zsh integration to contain %q", expected)
		}
	}
	if strings.Contains(script, `VUJA_CMD_START:$1`) {
		t.Fatal("history decision message must not send command text")
	}
}

func TestShellHooksPublishLoadedDotfilesFunctionsWithoutSourcingDotfiles(t *testing.T) {
	for _, shellName := range []string{"zsh", "bash", "fish"} {
		t.Run(shellName, func(t *testing.T) {
			script := shellInitScript(shellName, "/unused/vuja")
			for _, marker := range []string{"VUJA_FUNCTIONS_BEGIN", "VUJA_FUNCTION:", "VUJA_FUNCTIONS_END"} {
				if !strings.Contains(script, marker) {
					t.Fatalf("expected %s hook to publish %q", shellName, marker)
				}
			}
			if strings.Contains(script, `source "$HOME/.dotfiles`) || strings.Contains(script, `source ~/.dotfiles`) {
				t.Fatalf("%s hook must inspect already-loaded functions without sourcing dotfiles", shellName)
			}
			if shellName == "bash" && strings.Contains(script, `[[ $_vuja_function_source == "$HOME/.dotfiles/"* ]]`) {
				t.Fatal("bash hook must let Go canonicalize symlinked dotfile sources")
			}
			if shellName == "zsh" && strings.Contains(script, `[[ $_vuja_function_source == "${_vuja_dotfiles_root}/"* ]]`) {
				t.Fatal("zsh hook must let Go normalize source line suffixes before enforcing the dotfiles boundary")
			}
		})
	}
}

package root

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestZshInitPreservesLoadedHistoryHookDecision(t *testing.T) {
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	for name, setup := range map[string]string{
		"special function": "zshaddhistory() { return 1 }",
		"hook array":       "reject_history() { return 1 }; typeset -ga zshaddhistory_functions=(reject_history)",
	} {
		t.Run(name, func(t *testing.T) {
			integrationPath := filepath.Join(t.TempDir(), "init.zsh")
			if err := os.WriteFile(integrationPath, []byte(shellInitScript("zsh", "/unused/vuja")), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(path, "-f", "-c", setup+`
source "$1"
zshaddhistory $'private command\n'
_vuja_preexec 'private command'
`, "vuja-zsh-policy-test", integrationPath)
			command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=1")
			output, err := command.Output()
			if err != nil {
				t.Fatalf("zsh history-policy integration failed: %v", err)
			}
			if !strings.Contains(string(output), commandStartIgnoreMessage+"\x00") {
				t.Fatalf("expected rejected zsh history entry to be ignored, got %q", output)
			}
		})
	}
}

func TestZshInitPreservesHistoryPoliciesLoadedBetweenHookSources(t *testing.T) {
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	integrationPath := filepath.Join(t.TempDir(), "init.zsh")
	if err := os.WriteFile(integrationPath, []byte(shellInitScript("zsh", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "-f", "-c", `
source "$1"
zshaddhistory() { return 0 }
reject_late_history() { return 1 }
typeset -ga zshaddhistory_functions=(reject_late_history)
source "$1"
zshaddhistory $'private command\n'
_vuja_preexec 'private command'
`, "vuja-zsh-late-policy-test", integrationPath)
	command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("zsh late history-policy integration failed: %v", err)
	}
	if !strings.Contains(string(output), commandStartIgnoreMessage+"\x00") {
		t.Fatalf("expected late zsh history policy to remain effective, got %q", output)
	}
}

func TestBashInitReportsHistoryControlAndIgnorePolicies(t *testing.T) {
	script := shellInitScript("bash", "/unused/vuja")
	for _, expected := range []string{"HISTCONTROL", "HISTIGNORE", "HISTCMD", "_vuja_previous_histcmd", "[[ -o history"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("expected Bash integration to contain %q", expected)
		}
	}
	if strings.Contains(script, "BASH_COMMAND") {
		t.Fatal("Bash history policy must use the shell's complete-line history decision, not the current simple command")
	}
}

func TestFishInitPreservesTheLoadedHistoryPolicyDecision(t *testing.T) {
	script := shellInitScript("fish", "/unused/vuja")
	for _, expected := range []string{
		"fish_should_add_to_history",
		"_vuja_original_fish_should_add_to_history",
		"_vuja_history_policy_status",
		"fish_private_mode",
		"VUJA_CMD_START:IGNORE",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("expected Fish integration to contain %q", expected)
		}
	}
}

func TestFishInitReportsLoadedHistoryPolicyDecision(t *testing.T) {
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	integrationPath := filepath.Join(t.TempDir(), "init.fish")
	if err := os.WriteFile(integrationPath, []byte(shellInitScript("fish", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "--no-config", "-c", `
function fish_should_add_to_history
    return 1
end
source "$argv[1]"
fish_should_add_to_history 'private command'
emit fish_preexec 'private command'
`, integrationPath)
	command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fish history-policy integration failed: %v", err)
	}
	if !strings.Contains(string(output), commandStartIgnoreMessage+"\x00") {
		t.Fatalf("expected rejected fish history entry to be ignored, got %q", output)
	}
}

func TestFishInitPreservesHistoryPolicyLoadedBetweenHookSources(t *testing.T) {
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	integrationPath := filepath.Join(t.TempDir(), "init.fish")
	if err := os.WriteFile(integrationPath, []byte(shellInitScript("fish", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "--no-config", "-c", `
source "$argv[1]"
function fish_should_add_to_history
    return 1
end
source "$argv[1]"
fish_should_add_to_history 'private command'
emit fish_preexec 'private command'
`, integrationPath)
	command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fish late history-policy integration failed: %v", err)
	}
	if !strings.Contains(string(output), commandStartIgnoreMessage+"\x00") {
		t.Fatalf("expected late fish history policy to remain effective, got %q", output)
	}
}

func TestFishInitCanBeResourcedWithoutRecursingHistoryPolicy(t *testing.T) {
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	integrationPath := filepath.Join(t.TempDir(), "init.fish")
	if err := os.WriteFile(integrationPath, []byte(shellInitScript("fish", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--no-config", "-c", `
source "$argv[1]"
source "$argv[1]"
fish_should_add_to_history 'public command'
echo $status
`, integrationPath)
	command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fish repeated history-policy integration failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "0" {
		t.Fatalf("expected unchanged fish history policy to remain non-recursive, got %q", output)
	}
}

func TestManagedShellProtocolAcknowledgesDurableHistoryWithoutFlushingShellHistory(t *testing.T) {
	for _, shellName := range []string{"zsh", "bash", "fish"} {
		t.Run(shellName, func(t *testing.T) {
			script := shellInitScript(shellName, "/unused/vuja")
			if !strings.Contains(script, "VUJA_HISTORY_ACK_FD") {
				t.Fatalf("expected %s managed protocol to wait for durable history", shellName)
			}
			for _, unsafeFlush := range []string{"fc -AI", "history -a", "history save", "VUJA_HISTORY_MIRROR"} {
				if strings.Contains(script, unsafeFlush) {
					t.Fatalf("expected %s managed protocol not to flush unrelated native history with %q", shellName, unsafeFlush)
				}
			}
		})
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

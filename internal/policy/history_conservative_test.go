package policy

import "testing"

func TestHistoryCommandConservativelyExcludesForgettingText(t *testing.T) {
	for _, command := range []string{
		"vuja history forget --command 'fixture-command'",
		"! VUJA_MODE=local vuja history forget --id first",
		"time VUJA_MODE=local vuja history forget --id first",
		"env VUJA_MODE=local vuja history forget --id first",
		"command -p -- /tmp/bin/vuja history forget --id first",
		"printf 'vuja history forget --command fixture-command'",
		"printf \"vuja history forget\"",
		"printf 'env vuja history forget --id first'",
		"printf before # vuja history forget --id first",
		"printf before > vuja history forget --id first",
		"command -v vuja history forget --id first",
		"env printf vuja history forget --id first",
		"'VUJA_MODE=local' vuja history forget --id first",
		"VUJA_MODE\\=local vuja history forget --id first",
		"printf true\\; vuja history forget --id first",
		"true; 'vuja' h\"ist\"ory f\\orget --id first",
		"/tmp/bin/vuja --shell zsh history --debug forget --id first",
		"vuja history --shell=zsh -d forget --id first",
		"vuja history -dszsh forget --id first",
		"vuja --shell 'zsh' history --debug=false forget --id first",
		"vuja history --shell forget stats",
		"vuja history --debug explain forget",
		"vuja history -- --debug forget",
		"vuja\thistory\tforget --id first",
		"printf 'vuja\nhistory\nforget --id first'",
	} {
		t.Run(command, func(t *testing.T) {
			if _, allowed := HistoryCommand(command, false); allowed {
				t.Fatal("forgetting-command text was eligible for persistent history")
			}
		})
	}
	for _, command := range []string{
		"vuja history stats", "vuja history forgetful", "printf vuja-history-forget",
		"printf 'forget history vuja'", "git status", "ssh forge@fixture",
	} {
		if got, allowed := HistoryCommand(command, false); !allowed || got != command {
			t.Fatalf("ordinary command changed under the conservative privacy filter: %q", command)
		}
	}
}

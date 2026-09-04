package root

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
)

func TestResolveShellHistoryPathUsesAnExplicitConfiguredLocation(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "brian")

	configured, err := resolveShellHistoryPath(home, "zsh", "~/state/zsh/history")
	if err != nil {
		t.Fatalf("resolveShellHistoryPath() error = %v", err)
	}
	if want := filepath.Join(home, "state", "zsh", "history"); configured != want {
		t.Fatalf("resolveShellHistoryPath() = %q; want %q", configured, want)
	}
	if fallback, err := resolveShellHistoryPath(home, "zsh", ""); err != nil || fallback != filepath.Join(home, ".zsh_history") {
		t.Fatalf("expected an empty optional path to use the shell convention, got %q, %v", fallback, err)
	}
	if _, err := resolveShellHistoryPath(home, "zsh", "relative/history"); err == nil {
		t.Fatal("expected a relative shell-history path to be rejected")
	}
}

func TestPreferVujaExecutionsDeduplicatesOneToOneAndPreservesRepeats(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	native := []scoring.HistoryEvent{{
		EventKey: "vuja:1", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", StartedAt: started, Duration: time.Second, ExitCode: 0, HasExitCode: true, Source: "vuja",
	}}
	imported := []integration.HistoryEntry{
		{ID: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started.Add(100 * time.Millisecond), Duration: time.Second, ExitCode: 0, HasExitCode: true},
		{ID: "atuin:2", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started.Add(200 * time.Millisecond), Duration: time.Second, ExitCode: 0, HasExitCode: true},
	}
	kept := preferVujaExecutions(imported, native)
	if len(kept) != 1 || kept[0].ID != "atuin:2" {
		t.Fatalf("expected one imported counterpart removed and the repeat preserved, got %+v", kept)
	}
}

func TestPreferVujaExecutionsTreatsMigratedNativeHistoryAsAuthoritative(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	native := []scoring.HistoryEvent{{
		EventKey: "legacy:1", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", StartedAt: started, Source: "legacy-vuja", Imported: false,
	}}
	imported := []integration.HistoryEntry{{
		ID: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started,
	}}
	if kept := preferVujaExecutions(imported, native); len(kept) != 0 {
		t.Fatalf("expected migrated Vuja history to win over an imported duplicate, got %+v", kept)
	}
}

func TestPreferExternalExecutionsUsesRicherMetadataAndPreservesExtraRepeats(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	imported := []integration.HistoryEntry{
		{ID: "zsh:1", Command: "ssh forge@api", StartedAt: started, Source: "zsh"},
		{ID: "zsh:2", Command: "ssh forge@api", StartedAt: started, Source: "zsh"},
		{ID: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started, ExitCode: 0, HasExitCode: true, Source: "atuin"},
	}

	kept := preferExternalExecutions(imported)

	if len(kept) != 2 || kept[0].ID != "atuin:1" || kept[1].ID != "zsh:2" {
		t.Fatalf("expected Atuin metadata plus one legitimate shell repeat, got %+v", kept)
	}
}

func TestConfiguredHistoryIntegrationsDefaultToDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	if historyIntegrationsEnabled(cfg.History) {
		t.Fatal("expected a clean Vuja installation to avoid external history adapters")
	}
	if cfg.Suggestions.ImportZoxide {
		t.Fatal("expected a clean Vuja installation to avoid the optional Zoxide adapter")
	}
}

func TestHistoryMutationCommandsRefreshTheManagedSession(t *testing.T) {
	for _, command := range []string{"vuja history clear --confirm", "/usr/local/bin/vuja history prune --max 3", "vuja history import atuin"} {
		if !isHistoryMutationCommand(command) {
			t.Fatalf("expected %q to refresh canonical history", command)
		}
	}
	if !isHistoryClearCommand("/usr/local/bin/vuja history clear --confirm") || isHistoryClearCommand("vuja history prune --max 3") {
		t.Fatal("expected clear-specific lifecycle handling only for history clear")
	}
	for command, expected := range map[string]int{"vuja history prune --max 3": 3, "vuja history prune --max=7": 7} {
		limit, ok := historyPruneLimit(command)
		if !ok || limit != expected {
			t.Fatalf("historyPruneLimit(%q) = %d, %v; want %d, true", command, limit, ok, expected)
		}
	}
	for _, command := range []string{"vuja history stats", "echo vuja history clear", "history clear"} {
		if isHistoryMutationCommand(command) {
			t.Fatalf("did not expect %q to refresh canonical history", command)
		}
	}
}

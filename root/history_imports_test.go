package root

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/faustbrian/vuja/v2/internal/scoring"
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

func TestExternalHistoryImportDeduplicatesOneToOneAndPreservesRepeats(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	native := []scoring.HistoryEvent{{
		EventKey: "vuja:1", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", StartedAt: started, Duration: time.Second, ExitCode: 0, HasExitCode: true, Source: "vuja",
	}}
	imported := []integration.HistoryEntry{
		{ID: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started.Add(100 * time.Millisecond), Duration: time.Second, ExitCode: 0, HasExitCode: true, Source: "atuin"},
		{ID: "atuin:2", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started.Add(200 * time.Millisecond), Duration: time.Second, ExitCode: 0, HasExitCode: true, Source: "atuin"},
	}
	kept := retainedExternalHistoryFixture(t, native, imported)
	if len(kept) != 1 || kept[0].EventKey != "atuin:2" {
		t.Fatalf("expected one imported counterpart removed and the repeat preserved, got %+v", kept)
	}
}

func TestExternalHistoryImportTreatsMigratedNativeHistoryAsAuthoritative(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	native := []scoring.HistoryEvent{{
		EventKey: "legacy:1", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", StartedAt: started, Source: "legacy-vuja", Imported: false,
	}}
	imported := []integration.HistoryEntry{{
		ID: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started, Source: "atuin",
	}}
	if kept := retainedExternalHistoryFixture(t, native, imported); len(kept) != 0 {
		t.Fatalf("expected migrated Vuja history to win over an imported duplicate, got %+v", kept)
	}
}

func retainedExternalHistoryFixture(t *testing.T, native []scoring.HistoryEvent, imported []integration.HistoryEntry) []scoring.HistoryEvent {
	t.Helper()
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, event := range native {
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	converted := make([]scoring.HistoryEvent, len(imported))
	for index, event := range imported {
		converted[index] = scoringHistoryEvent(event)
	}
	if err := store.ReplaceExternalHistoryEvents(t.Context(), converted); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var kept []scoring.HistoryEvent
	for _, event := range events {
		if event.Imported {
			kept = append(kept, event)
		}
	}
	return kept
}

func TestPreferExternalExecutionsUsesRicherMetadataAndPreservesExtraRepeats(t *testing.T) {
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	imported := []scoring.HistoryEvent{
		{EventKey: "zsh:1", Command: "ssh forge@api", StartedAt: started, Source: "zsh"},
		{EventKey: "zsh:2", Command: "ssh forge@api", StartedAt: started, Source: "zsh"},
		{EventKey: "atuin:1", Command: "ssh forge@api", Cwd: "/repo", StartedAt: started, ExitCode: 0, HasExitCode: true, Source: "atuin"},
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReplaceAdapterHistoryEvents(t.Context(), imported); err != nil {
		t.Fatal(err)
	}
	kept, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	ids := make(map[string]bool)
	for _, event := range kept {
		ids[event.EventKey] = true
	}
	if len(kept) != 2 || !ids["atuin:1"] || !ids["zsh:2"] {
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

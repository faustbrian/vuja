package integration

import (
	"strings"
	"testing"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/spec"
)

// QA scenario 5: controls must be discoverable at the actual suggestion surface,
// without changing the command that Enter would execute.
func TestSuggestionMenuMakesRowControlsDiscoverable(t *testing.T) {
	before := config.Get()
	config.Init(config.DefaultConfig())
	t.Cleanup(func() { config.Init(before) })
	o := NewOverlay(false)
	o.SetTerminalSize(180, 30)
	o.SetQueryAndItems("git", []spec.Suggestion{{ID: "status", Cmd: "git status"}})
	o.FocusFirst()
	for _, expected := range []string{"<F5>", " Pin", "<F6>", " Block", "<F9>", " Why", "Run selected"} {
		if got := o.Render(); !strings.Contains(got, expected) {
			t.Fatalf("selected row does not expose %q: %q", expected, got)
		}
	}
	if got := o.GetCurrentCmd(); got != "git status" {
		t.Fatalf("rendering controls changed the execution target: %q", got)
	}
}

func TestRankingExplanationDoesNotInventMissingEvidence(t *testing.T) {
	o := NewOverlay(false)
	o.SetTerminalSize(180, 30)
	o.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git status", Confidence: 99}})
	o.FocusFirst()
	o.ToggleRankingExplanation()
	if got := o.Render(); !strings.Contains(got, "No recorded ranking evidence") || strings.Contains(got, "99") || !strings.Contains(got, "Run selected") {
		t.Fatalf("ranking view invented confidence or changed Enter semantics: %q", got)
	}
}

func TestPreferenceRefreshPreservesChronologicalHistoryAndClearsBlockedSelection(t *testing.T) {
	o := NewOverlay(false)
	o.SetRecentHistory([]RichHistoryResult{
		{HistoryEntry: HistoryEntry{ID: "new", Command: "new"}},
		{HistoryEntry: HistoryEntry{ID: "middle", Command: "middle"}},
		{HistoryEntry: HistoryEntry{ID: "old", Command: "old"}},
	})
	o.RefreshPreferences(func(command string) bool { return command == "new" })
	if o.GetCurrentCmd() != "" || o.GetUserNavigated() || len(o.HistoryResults) != 2 || o.HistoryResults[0].ID != "old" || o.HistoryResults[1].ID != "middle" {
		t.Fatalf("blocking changed history order, identity, or selection: %+v", o.HistoryResults)
	}
}

func TestSuggestionPreferencesRejectAIInjectionAfterBlockPublication(t *testing.T) {
	before := config.Get()
	config.Init(config.DefaultConfig())
	t.Cleanup(func() { config.Init(before) })
	o := NewOverlay(false)
	o.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git diff", Confidence: 10}})
	captured := spec.Suggestion{Cmd: "git status", Source: "ai", Confidence: 99}
	if policy.Blocked(captured.Cmd) {
		t.Fatal("fixture began blocked")
	}
	// The old AI callback has already passed its outer policy check.
	config.PublishSuggestionPreferences(config.SuggestionsConfig{ExactBlocks: []string{captured.Cmd}})
	o.RefreshPreferences(policy.Blocked)
	if o.InjectAISuggestion(captured) || len(o.Items) != 1 || o.Items[0].Cmd != "git diff" {
		t.Fatal("old AI result bypassed the published block at injection")
	}
}

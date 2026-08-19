package root

import "testing"

func TestHistorySearchEditsQueryWithoutChangingOriginalBuffer(t *testing.T) {
	var search historySearchSession
	search.Open("git status")
	search.Append("dock")
	search.Backspace()

	state := search.State()
	if state.Query != "doc" {
		t.Fatalf("expected query doc, got %q", state.Query)
	}
	if state.Original != "git status" {
		t.Fatalf("expected original buffer preserved, got %q", state.Original)
	}
}

func TestHistorySearchCyclesScopesAndSuccessfulFilter(t *testing.T) {
	var search historySearchSession
	search.Open("")

	if state := search.State(); state.Scope != historyScopeDirectory {
		t.Fatalf("expected directory scope, got %q", state.Scope)
	}
	search.CycleScope()
	if state := search.State(); state.Scope != historyScopeProject {
		t.Fatalf("expected project scope, got %q", state.Scope)
	}
	search.CycleScope()
	if state := search.State(); state.Scope != historyScopeGlobal {
		t.Fatalf("expected global scope, got %q", state.Scope)
	}
	search.CycleScope()
	if state := search.State(); state.Scope != historyScopeMachine {
		t.Fatalf("expected machine scope, got %q", state.Scope)
	}
	search.CycleScope()
	if state := search.State(); state.Scope != historyScopeSession {
		t.Fatalf("expected session scope, got %q", state.Scope)
	}
	search.CycleScope()
	if state := search.State(); state.Scope != historyScopeDirectory {
		t.Fatalf("expected scope cycle to return to directory, got %q", state.Scope)
	}
	search.ToggleSuccessfulOnly()
	if !search.State().SuccessfulOnly {
		t.Fatal("expected successful-only filtering")
	}
}

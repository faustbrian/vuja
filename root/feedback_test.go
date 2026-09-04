package root

import (
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/scoring"
)

func TestSuggestionFeedbackFinishesBeforeLaterHistoryDeletion(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := t.TempDir()
	recordSuggestionFeedbackToStore(t.Context(), store, []suggestionFeedbackEvent{{command: "ssh forge@api", kind: "typed"}}, cwd)

	feedback, err := store.QueryFeedback(t.Context(), cwd, "", "ssh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(feedback) != 1 || feedback[0].Typed != 1 {
		t.Fatalf("expected feedback to be durable when recording returns, got %+v", feedback)
	}
	if err := store.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	feedback, err = store.QueryFeedback(t.Context(), cwd, "", "ssh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(feedback) != 0 {
		t.Fatalf("expected later deletion to remain final, got %+v", feedback)
	}
}

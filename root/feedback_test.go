package root

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/scoring"
)

func TestSuggestionFeedbackCannotReenterAfterItsExecutionWasForgotten(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := t.TempDir()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	execution := scoring.HistoryEvent{EventKey: "feedback:forgotten", Command: "ssh forge@api", Cwd: cwd, Source: "vuja", StartedAt: at}
	if err := store.RecordHistorySubmission(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	execution.CompletedAt, execution.HasExitCode = at.Add(time.Second), true
	if completed, err := store.CompleteHistoryEvent(t.Context(), execution); err != nil || !completed {
		t.Fatalf("completion fixture: completed=%v error=%v", completed, err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{execution.EventKey}); err != nil {
		t.Fatal(err)
	}
	// The wrapper has already received a successful completion, but forgetting
	// wins before its pending feedback is delivered to the real store adapter.
	recordSuggestionFeedbackToStore(t.Context(), store, []suggestionFeedbackEvent{
		{command: execution.Command, kind: "typed"},
		{command: "ssh forge@offered", kind: "dismissed"},
	}, cwd, execution.EventKey)
	if feedback, err := store.QueryFeedback(t.Context(), cwd, "", "ssh", 10); err != nil || len(feedback) != 0 {
		t.Fatalf("delayed completion restored forgotten suggestion evidence: %+v %v", feedback, err)
	}
}

func TestSuggestionFeedbackForgettingOneExecutionPreservesDuplicateEvidence(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := t.TempDir()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"feedback:first", "feedback:second"} {
		execution := scoring.HistoryEvent{EventKey: id, Command: "ssh forge@api", Cwd: cwd, Source: "vuja", StartedAt: at.Add(time.Duration(index) * time.Minute)}
		if err := store.RecordHistorySubmission(t.Context(), execution); err != nil {
			t.Fatal(err)
		}
		execution.CompletedAt, execution.HasExitCode = execution.StartedAt.Add(time.Second), true
		if completed, err := store.CompleteHistoryEvent(t.Context(), execution); err != nil || !completed {
			t.Fatalf("completion fixture: completed=%v error=%v", completed, err)
		}
		recordSuggestionFeedbackToStore(t.Context(), store, []suggestionFeedbackEvent{
			{command: execution.Command, kind: "typed"},
			{command: "ssh forge@offered", kind: "dismissed"},
		}, cwd, execution.EventKey)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{"feedback:first"}); err != nil {
		t.Fatal(err)
	}
	feedback, err := store.QueryFeedback(t.Context(), cwd, "", "ssh", 10)
	if err != nil || len(feedback) != 2 {
		t.Fatalf("forgetting one execution erased the duplicate's feedback: %+v %v", feedback, err)
	}
	for _, entry := range feedback {
		if entry.Cmd == "ssh forge@api" && entry.Typed != 1 || entry.Cmd == "ssh forge@offered" && entry.Dismissed != 1 {
			t.Fatalf("survivor must retain exactly its own feedback: %+v", entry)
		}
	}
}

func TestSuggestionFeedbackFinishesBeforeLaterHistoryDeletion(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := t.TempDir()
	execution := scoring.HistoryEvent{EventKey: "feedback:kept", Command: "ssh forge@api", Cwd: cwd, Source: "vuja", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	if err := store.RecordHistorySubmission(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	execution.CompletedAt, execution.HasExitCode = execution.StartedAt.Add(time.Second), true
	if completed, err := store.CompleteHistoryEvent(t.Context(), execution); err != nil || !completed {
		t.Fatalf("completion fixture: completed=%v error=%v", completed, err)
	}
	recordSuggestionFeedbackToStore(t.Context(), store, []suggestionFeedbackEvent{{command: "ssh forge@api", kind: "typed"}}, cwd, execution.EventKey)

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

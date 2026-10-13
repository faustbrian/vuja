package scoring

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCanonicalHistoryEffectDistinguishesSubmissionCompletionAndAbsence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	entry := HistoryEvent{EventKey: "effect-entry", Command: "private-command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: "submitted"}
	if err := store.RecordHistorySubmission(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), entry.EventKey, false); !present || err != nil {
		t.Fatalf("canonical submission was not confirmed: %v %v", present, err)
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), entry.EventKey, true); present || err != nil {
		t.Fatalf("submission was confused with a completed outcome: %v %v", present, err)
	}
	entry.HasExitCode, entry.ExitCode = true, 7
	entry.CompletedAt = entry.SubmittedAt.Add(time.Second)
	if completed, err := store.CompleteHistoryEvent(t.Context(), entry); !completed || err != nil {
		t.Fatalf("fixture completion failed: %v %v", completed, err)
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), entry.EventKey, true); !present || err != nil {
		t.Fatalf("actual completion was not confirmed: %v %v", present, err)
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), "missing", false); present || err != nil {
		t.Fatalf("missing event was falsely confirmed: %v %v", present, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if present, err := store.HasCanonicalHistoryEffect(cancelled, entry.EventKey, false); present || err == nil {
		t.Fatalf("cancelled inspection was treated as canonical evidence: %v %v", present, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), entry.EventKey, false); present || err == nil {
		t.Fatalf("closed database was treated as canonical evidence: %v %v", present, err)
	}
}

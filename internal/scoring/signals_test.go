package scoring

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectSignals(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{}"), 0644)

	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	_ = store.Record(context.Background(), "npm run dev", tmpDir, 0)
	_ = store.Record(context.Background(), "npm test", "/other/dir", 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "npm run dev", tmpDir, 0)
	_ = store.RecordExactTransition(context.Background(), "git checkout feature", "npm run dev", tmpDir, 0)

	signals := CollectSignals(context.Background(), tmpDir, "npm", "npm", store, "git checkout feature", "git checkout")

	if !signals.Workspace.HasNodeProject {
		t.Error("expected HasNodeProject to be true in collected signals")
	}
	if len(signals.LocalFrecency) != 1 || signals.LocalFrecency[0].Cmd != "npm run dev" {
		t.Errorf("expected local frecency to contain 'npm run dev', got %v", signals.LocalFrecency)
	}
	if len(signals.GlobalFrecency) != 2 {
		t.Errorf("expected global frecency to contain 2 entries, got %d", len(signals.GlobalFrecency))
	}
	if !signals.TransitionIsLocal || len(signals.TransitionEntries) != 1 || signals.TransitionEntries[0].NextSkeleton != "npm run dev" {
		t.Errorf("expected transition entry 'npm run dev', got %v (isLocal=%v)", signals.TransitionEntries, signals.TransitionIsLocal)
	}
	if !signals.ExactTransitionIsLocal || len(signals.ExactTransitionEntries) != 1 || signals.ExactTransitionEntries[0].NextCommand != "npm run dev" {
		t.Errorf("expected exact transition entry 'npm run dev', got %v (isLocal=%v)", signals.ExactTransitionEntries, signals.ExactTransitionIsLocal)
	}
}

func TestFilterSignalsNarrowsCachedCommandSignals(t *testing.T) {
	signals := SignalSet{
		LocalFrecency: []FrecencyEntry{{Cmd: "git status"}, {Cmd: "go test ./..."}},
		Feedback:      []FeedbackEntry{{Cmd: "git status"}, {Cmd: "go test ./..."}},
		Outcomes:      []OutcomeEntry{{Cmd: "git status"}, {Cmd: "go test ./..."}},
	}
	filtered := filterSignals(signals, "git", "git")
	if len(filtered.LocalFrecency) != 1 || filtered.LocalFrecency[0].Cmd != "git status" {
		t.Fatalf("expected cached frecency to narrow to git, got %v", filtered.LocalFrecency)
	}
	if len(filtered.Feedback) != 1 || len(filtered.Outcomes) != 1 {
		t.Fatalf("expected feedback and outcomes to narrow with the query, got feedback=%v outcomes=%v", filtered.Feedback, filtered.Outcomes)
	}
}

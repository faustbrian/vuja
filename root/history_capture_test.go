package root

import (
	"testing"
	"time"
)

func TestCommandExecutionTrackerCapturesTimingAndScopeMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tracker := newCommandExecutionTracker(clock, "session-a", "devbox")

	submitted := tracker.Start("just deploy staging", "/repo", "zsh")
	if submitted.Command != "just deploy staging" || submitted.State != "running" || submitted.Shell != "zsh" {
		t.Fatalf("expected a complete running event before execution, got %+v", submitted)
	}
	now = now.Add(1250 * time.Millisecond)
	entry := tracker.Finish(17)

	if entry.StartedAt != now.Add(-1250*time.Millisecond) || entry.Duration != 1250*time.Millisecond {
		t.Fatalf("expected measured start and duration, got %+v", entry)
	}
	if !entry.HasExitCode || entry.ExitCode != 17 || entry.SessionID != "session-a" || entry.Host != "devbox" {
		t.Fatalf("expected exit and scope metadata, got %+v", entry)
	}
	if entry.ID == "" || entry.Source != "vuja" {
		t.Fatalf("expected a persistable native event identity, got %+v", entry)
	}
	if entry.ID != submitted.ID || entry.State != "failed" || !entry.CompletedAt.Equal(now) {
		t.Fatalf("expected completion to enrich the submitted event, got submitted=%+v completed=%+v", submitted, entry)
	}
}

func TestCommandExecutionTrackerPreservesExactAndNormalizedForms(t *testing.T) {
	tracker := newCommandExecutionTracker(time.Now, "session-a", "devbox")

	entry := tracker.StartRaw("printf value  ", "printf value", "/repo", "zsh")

	if entry.Command != "printf value  " || entry.NormalizedCommand != "printf value" {
		t.Fatalf("expected exact and normalized command forms to remain distinct, got %+v", entry)
	}
}

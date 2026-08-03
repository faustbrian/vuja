package root

import (
	"testing"
	"time"
)

func TestCommandExecutionTrackerCapturesTimingAndScopeMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tracker := newCommandExecutionTracker(clock, "session-a", "devbox")

	tracker.Start()
	now = now.Add(1250 * time.Millisecond)
	entry := tracker.Finish("just deploy staging", "/repo", 17)

	if entry.StartedAt != now.Add(-1250*time.Millisecond) || entry.Duration != 1250*time.Millisecond {
		t.Fatalf("expected measured start and duration, got %+v", entry)
	}
	if !entry.HasExitCode || entry.ExitCode != 17 || entry.SessionID != "session-a" || entry.Host != "devbox" {
		t.Fatalf("expected exit and scope metadata, got %+v", entry)
	}
	if entry.ID == "" || entry.Source != "vuja" {
		t.Fatalf("expected a persistable native event identity, got %+v", entry)
	}
}

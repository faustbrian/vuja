package root

import (
	"strings"
	"testing"
	"time"
)

func TestLatencyRecorderRetainsBoundedSamplesAndComputesPercentiles(t *testing.T) {
	recorder := newLatencyRecorder()
	t.Cleanup(recorder.Close)
	for index := 1; index <= latencySampleLimit+10; index++ {
		recorder.record("total", time.Duration(index)*time.Millisecond)
	}
	snapshot := recorder.snapshotCopy()
	if got := len(snapshot.SamplesUS["total"]); got != latencySampleLimit {
		t.Fatalf("expected %d retained samples, got %d", latencySampleLimit, got)
	}
	if p95 := latencyPercentile(snapshot.SamplesUS["total"], .95); p95 <= 0 {
		t.Fatalf("expected a positive p95, got %s", p95)
	}
}

func TestLatencySnapshotIdentifiesSessionAndCacheTier(t *testing.T) {
	recorder := newLatencyRecorderWithSession("session-123", 42)
	t.Cleanup(recorder.Close)
	recorder.cache("fast", true)
	recorder.cache("full", false)
	snapshot := recorder.snapshotCopy()

	if snapshot.SessionID != "session-123" || snapshot.PID != 42 {
		t.Fatalf("expected session identity in snapshot, got %#v", snapshot)
	}
	if snapshot.Cache["fast"].Hits != 1 || snapshot.Cache["full"].Misses != 1 {
		t.Fatalf("expected cache results per tier, got %#v", snapshot.Cache)
	}
}

func TestLatencyRecorderCloseStopsWorkerAndIsIdempotent(t *testing.T) {
	recorder := newLatencyRecorderWithSession("close-test", 42)
	recorder.Close()
	recorder.Close()

	select {
	case <-recorder.done:
	default:
		t.Fatal("expected latency recorder worker to stop")
	}
}

func TestLatencySnapshotPathIsSessionSpecific(t *testing.T) {
	path, err := latencySnapshotPath("session/123", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "suggestion-latency-session-123-42.json") {
		t.Fatalf("expected a sanitized session-specific path, got %q", path)
	}
}

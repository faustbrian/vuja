package root

import (
	"strings"
	"testing"
)

func TestWatchdogStderrDisplaySuppressesMallocStackLoggingNoise(t *testing.T) {
	t.Parallel()

	var display watchdogStderrDisplay
	got := string(display.Consume([]byte("before\nvuja(8390) MallocStackLogging: can't turn off malloc stack logging because it was not enabled.\nafter\n"), true))

	if got != "before\nafter\n" {
		t.Fatalf("expected only the allocator diagnostic to be suppressed, got %q", got)
	}
}

func TestWatchdogStderrDisplaySuppressesNoiseSplitAcrossReads(t *testing.T) {
	t.Parallel()

	var display watchdogStderrDisplay
	var got strings.Builder
	got.Write(display.Consume([]byte("vuja(8407) MallocStackLogging: can't turn off malloc"), false))
	got.Write(display.Consume([]byte(" stack logging because it was not enabled.\nkept\n"), false))
	got.Write(display.Consume(nil, true))

	if got.String() != "kept\n" {
		t.Fatalf("expected a split allocator diagnostic to be suppressed, got %q", got.String())
	}
}

func TestWatchdogStderrDisplayPreservesNearMatchesAndUnterminatedOutput(t *testing.T) {
	t.Parallel()

	const input = "helper(8407) MallocStackLogging: can't turn off malloc stack logging because it was not enabled.\nvuja(8407) MallocStackLogging: a different diagnostic\nunterminated"
	var display watchdogStderrDisplay
	got := string(display.Consume([]byte(input), true))

	if got != input {
		t.Fatalf("expected non-Vuja diagnostics and ordinary stderr to be preserved, got %q", got)
	}
}

func TestWatchdogStderrDisplayDoesNotDelayOrdinaryPartialLines(t *testing.T) {
	t.Parallel()

	var display watchdogStderrDisplay
	if got := string(display.Consume([]byte("progress"), false)); got != "progress" {
		t.Fatalf("expected ordinary partial stderr to be forwarded immediately, got %q", got)
	}
	if got := string(display.Consume([]byte(" continues\n"), false)); got != " continues\n" {
		t.Fatalf("expected the remainder of an ordinary stderr line to be forwarded immediately, got %q", got)
	}
}

func TestWatchdogStderrDisplayResetDiscardsBufferedCandidate(t *testing.T) {
	t.Parallel()

	var display watchdogStderrDisplay
	if got := display.Consume([]byte("vu"), false); len(got) != 0 {
		t.Fatalf("expected incomplete stderr line to remain buffered, got %q", got)
	}

	display.Reset()
	if got := string(display.Consume([]byte("ordinary\n"), true)); got != "ordinary\n" {
		t.Fatalf("expected reset to discard only pending bytes, got %q", got)
	}
}

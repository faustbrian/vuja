package integration

import (
	"fmt"
	"testing"
	"time"
)

func canonicalHistoryBenchmarkEntries() []HistoryEntry {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entries := make([]HistoryEntry, 100_000)
	for index := range entries {
		command := fmt.Sprintf("command-%05d", index%10_000)
		if index%2500 == 0 {
			command = fmt.Sprintf("ssh forge@host-%02d", index/2500)
		}
		entries[index] = HistoryEntry{
			ID: fmt.Sprintf("vuja:benchmark:%06d", index), Command: command,
			StartedAt: now.Add(time.Duration(index) * time.Second), Source: "vuja", State: HistoryStateCompleted,
		}
	}
	return entries
}

func BenchmarkCanonicalHistoryColdPublish100K(b *testing.B) {
	entries := canonicalHistoryBenchmarkEntries()
	b.ResetTimer()
	for range b.N {
		PublishCanonicalHistory(entries)
	}
}

func BenchmarkCanonicalHistoryWarmSSHPrefix100K(b *testing.B) {
	PublishCanonicalHistory(canonicalHistoryBenchmarkEntries())
	b.ResetTimer()
	for range b.N {
		_, _ = SearchHistory("ssh", nil)
	}
}

func BenchmarkCanonicalHistoryIncrementalSSHPrefix100K(b *testing.B) {
	PublishCanonicalHistory(canonicalHistoryBenchmarkEntries())
	b.ResetTimer()
	for range b.N {
		resetIncrementalHistorySearchLocked()
		_, _ = SearchHistory("s", nil)
		_, _ = SearchHistory("ss", nil)
		_, _ = SearchHistory("ssh", nil)
	}
}

func BenchmarkCanonicalHistoryPublishSubmission100K(b *testing.B) {
	PublishCanonicalHistory(canonicalHistoryBenchmarkEntries())
	startedAt := time.Date(2026, time.August, 31, 13, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for index := range b.N {
		PublishCanonicalHistoryEntry(HistoryEntry{
			ID: fmt.Sprintf("vuja:submitted:%d", index%1024), Command: "ssh forge@live",
			StartedAt: startedAt.Add(time.Duration(index) * time.Nanosecond), Source: "vuja", State: HistoryStateRunning,
		})
	}
}

package integration

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkSearchRichHistory10K(b *testing.B) {
	entries, now := richHistoryBenchmarkEntries()
	options := RichHistorySearchOptions{Now: now, Limit: 100}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = SearchRichHistory(entries, "deploy", options)
	}
}

func BenchmarkSearchCurrentRichHistoryEmpty10K(b *testing.B) {
	entries, now := richHistoryBenchmarkEntries()
	options := RichHistorySearchOptions{Now: now, Limit: 100}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = searchRichHistory(entries, "", options, true)
	}
}

func richHistoryBenchmarkEntries() ([]HistoryEntry, time.Time) {
	entries := make([]HistoryEntry, 10_000)
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	for index := range entries {
		command := fmt.Sprintf("git status --short path/to/package/%05d", index)
		if index%100 == 0 {
			command = fmt.Sprintf("kubectl rollout status deploy/service-%05d", index)
		}
		entries[index] = HistoryEntry{
			ID:        fmt.Sprintf("event-%05d", index),
			Command:   command,
			Cwd:       "/repo",
			StartedAt: now.Add(-time.Duration(index) * time.Second),
		}
	}
	prepareHistoryEntries(entries)
	return entries, now
}

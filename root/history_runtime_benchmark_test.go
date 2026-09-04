package root

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/scoring"
)

func BenchmarkCanonicalHistoryColdStoreLoad100K(b *testing.B) {
	store, err := scoring.NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	events := make([]scoring.HistoryEvent, 100_000)
	for index := range events {
		command := fmt.Sprintf("command-%05d", index%10_000)
		if index%2500 == 0 {
			command = fmt.Sprintf("ssh forge@host-%02d", index/2500)
		}
		events[index] = scoring.HistoryEvent{
			EventKey: fmt.Sprintf("atuin:benchmark:%06d", index), Command: command, NormalizedCommand: command,
			SubmittedAt: now.Add(time.Duration(index) * time.Second),
			StartedAt:   now.Add(time.Duration(index) * time.Second),
			Source:      "atuin", State: "completed", ExitCode: 0, HasExitCode: true, Imported: true,
		}
	}
	if err := store.ReplaceImportedHistoryEvents(b.Context(), events); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := publishCanonicalStoreHistory(b.Context(), store); err != nil {
			b.Fatal(err)
		}
	}
}

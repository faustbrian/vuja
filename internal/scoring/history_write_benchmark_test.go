package scoring

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Uncontended durable submission, not shell acknowledgement or first paint.
// Store setup is excluded; each operation includes the actual FULL-sync commit.
func BenchmarkHistorySubmissionWriteBudget(b *testing.B) {
	for _, name := range []string{"interactive", "background"} {
		b.Run(name, func(b *testing.B) {
			store, err := NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				ctx := b.Context()
				cancel := func() {}
				if name == "interactive" {
					ctx, cancel = context.WithTimeout(ctx, time.Second)
				}
				err := store.RecordHistorySubmission(ctx, HistoryEvent{
					EventKey: "budget:" + strconv.Itoa(index), Command: "ssh forge@fixture",
					Cwd: "/fixture", Source: "vuja", SubmittedAt: at, StartedAt: at,
				})
				cancel()
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
		})
	}
}

package scoring

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkSignalSnapshotFixture(b *testing.B) {
	store, err := NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO imported_history_entries
		(cmd, cwd, count, last_used, source) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	for index := range 5_000 {
		command := fmt.Sprintf("git command-%04d", index%500)
		directory := fmt.Sprintf("/repo/package-%02d", (index/500)%50)
		if _, err := statement.Exec(command, directory, index%100+1, canonicalTimestamp(time.Now().Add(-time.Duration(index)*time.Minute)), "fixture"); err != nil {
			b.Fatal(err)
		}
	}
	_ = statement.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	b.Run("database-snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			snapshot, err := store.QuerySignalSnapshot(context.Background(), "/repo/package-00", "/repo", "git", 50, "git status", "git")
			if err != nil || len(snapshot.Global) == 0 {
				b.Fatalf("snapshot failed: %v", err)
			}
		}
	})

	b.Run("warm-prefix-cache", func(b *testing.B) {
		InvalidateSignalCache()
		_ = CollectSignals(context.Background(), "/repo/package-00", "git", "git", store, "git status", "git")
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			signals := CollectSignals(context.Background(), "/repo/package-00", "git c", "git", store, "git status", "git")
			if len(signals.GlobalFrecency) == 0 {
				b.Fatal("expected cached fixture signals")
			}
		}
	})
}

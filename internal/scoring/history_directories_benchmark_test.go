package scoring

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Completion-only write cost, excluding canonical submission/completion and
// fixture setup. Concentrated visits stress one heavily used destination;
// dispersed visits expose per-destination lookup cost as total history grows.
// Legacy path-only writes are a cost baseline, not a privacy-correct substitute.
func BenchmarkHistoryDirectoryAttribution(b *testing.B) {
	for _, size := range []int{1_000, 10_000} {
		for _, shape := range []string{"concentrated", "dispersed"} {
			for _, mode := range []string{"legacy", "attributed", "attributed-no-path-index"} {
				b.Run(fmt.Sprintf("%d/%s/%s", size, shape, mode), func(b *testing.B) {
					store, err := NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(func() { _ = store.Close() })
					ctx := b.Context()
					tx, err := store.db.BeginTx(ctx, nil)
					if err != nil {
						b.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					at := canonicalTimestamp(time.Now().UTC().Add(-time.Hour))
					statement, err := tx.PrepareContext(ctx, `INSERT INTO history_events
(event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at, exit_code, source, state)
VALUES (?, 'z target', 'z target', '/bench', ?, ?, ?, 0, 'vuja', 'completed')`)
					if err != nil {
						b.Fatal(err)
					}
					defer func() { _ = statement.Close() }()
					target := "/bench/target"
					for index := range size + b.N {
						id := fmt.Sprintf("execution-%d", index)
						if _, err := statement.ExecContext(ctx, id, at, at, at); err != nil {
							b.Fatal(err)
						}
						if index >= size {
							continue
						}
						path := target
						if shape == "dispersed" && index%100 != 0 {
							path = fmt.Sprintf("/bench/other-%d", index%100)
						}
						// Legacy baseline visits have no execution-attributed
						// membership. Seeding both would falsely label the same
						// fixture executions as two disjoint generations.
						if mode != "legacy" {
							if _, err := tx.ExecContext(ctx, `INSERT INTO directory_navigation_visits (event_key, path) VALUES (?, ?)`, id, path); err != nil {
								b.Fatal(err)
							}
						}
					}
					if err := tx.Commit(); err != nil {
						b.Fatal(err)
					}
					if err := store.RefreshHistoryDirectories(ctx); err != nil {
						b.Fatal(err)
					}
					initial := size
					if shape == "dispersed" {
						initial = size / 100
					}
					if mode == "legacy" {
						if _, err := store.db.ExecContext(ctx, `INSERT INTO directory_navigation_index (path, count, recent_count, last_used) VALUES (?, ?, ?, ?)`, target, initial, initial, at); err != nil {
							b.Fatal(err)
						}
					}
					if mode == "attributed-no-path-index" {
						if _, err := store.db.ExecContext(ctx, `DROP INDEX idx_directory_navigation_visits_path`); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportAllocs()
					b.ResetTimer()
					for index := range b.N {
						if mode == "legacy" {
							err = store.RecordDirectory(ctx, target)
						} else {
							err = store.RecordHistoryDirectory(ctx, fmt.Sprintf("execution-%d", size+index), target)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					entries, err := store.QueryDirectories(ctx, target, 10, "frequent")
					if err != nil || len(entries) != 1 || entries[0].Count != initial+b.N {
						b.Fatalf("write benchmark did not preserve visit counts: %+v %v", entries, err)
					}
				})
			}
		}
	}
}

package scoring

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkHistoryNativeAdmissionWithForgottenImports(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 100_000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/indexed=%t", size, indexed), func(b *testing.B) {
				store, err := NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = store.Close() })
				ctx := b.Context()
				// Both variants start from identical schema/data and differ only
				// in the candidate admission index, including when it is shipped.
				if _, err := store.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_forgotten_native_command_time`); err != nil {
					b.Fatal(err)
				}
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				statement, err := tx.PrepareContext(ctx, `INSERT INTO forgotten_native_history
(identity_hash, command_hash, cwd_hash, started_at, origin_source_hash, forgotten_at)
VALUES (?, ?, '', ?, ?, ?)`)
				if err != nil {
					b.Fatal(err)
				}
				defer statement.Close()
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				for index := range size {
					if _, err := statement.ExecContext(ctx, forgottenHistoryIdentity(fmt.Sprintf("import:%d", index)),
						historyEvidenceHash("command", fmt.Sprintf("ssh forge@host-%d", index%100)),
						canonicalTimestamp(at.Add(time.Duration(index)*time.Second)),
						historyEvidenceHash("source", "atuin"), canonicalTimestamp(at.Add(time.Hour))); err != nil {
						b.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				var indexBuild time.Duration
				if indexed {
					started := time.Now()
					if _, err := store.db.ExecContext(ctx, `CREATE INDEX benchmark_forgotten_native_command_time
ON forgotten_native_history(command_hash, started_at)
WHERE origin_source_hash <> '' AND native_identity_hash = ''`); err != nil {
						b.Fatal(err)
					}
					indexBuild = time.Since(started)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for index := range b.N {
					if err := store.RecordHistorySubmission(ctx, HistoryEvent{
						EventKey: fmt.Sprintf("native:%d", index), Command: "git status", Source: "vuja",
						Cwd: "/repo", StartedAt: at.Add(48 * time.Hour),
					}); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if indexed {
					b.ReportMetric(float64(indexBuild.Microseconds()), "index-build-us")
				}
				if events, err := store.QueryAllHistoryEvents(ctx); err != nil || len(events) != b.N {
					b.Fatalf("native admission lost independent executions: count=%d expected=%d error=%v", len(events), b.N, err)
				}
				var pageCount, pageSize int
				if err := store.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
					b.Fatal(err)
				}
				if err := store.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(pageCount*pageSize), "logical-db-bytes")
			})
		}
	}
}

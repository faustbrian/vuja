package scoring

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Goal QA 3/8: compare the actual canonical writer and delayed-completion
// repair on identical long-session data. Only the candidate index differs.
// Run with bounded iterations, e.g. -benchtime=1x -count=3; no timing gate.
func BenchmarkHistoryArchivePortableOrder(b *testing.B) {
	for _, size := range []int{1_000, 10_000} {
		for _, operation := range []string{"submission", "late-completion"} {
			for _, indexed := range []bool{false, true} {
				b.Run(fmt.Sprintf("%d/%s/indexed=%t", size, operation, indexed), func(b *testing.B) {
					store, err := NewFrecencyStore(filepath.Join(b.TempDir(), "history.db"))
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(func() { _ = store.Close() })
					ctx := b.Context()
					if _, err := store.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_history_events_native_order`); err != nil {
						b.Fatal(err)
					}
					tx, err := store.db.BeginTx(ctx, nil)
					if err != nil {
						b.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					statement, err := tx.PrepareContext(ctx, `INSERT INTO history_events
(event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
 duration_ns, exit_code, source, session_id, state, history_order)
VALUES (?, ?, ?, '/bench', ?, ?, ?, 1000000, ?, 'vuja', 'bench-session', ?, ?)`)
					if err != nil {
						b.Fatal(err)
					}
					defer statement.Close()
					at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
					for index := range size {
						state, exit := "completed", any(0)
						if index == size/2 {
							state, exit = "interrupted", nil
						}
						command := fmt.Sprintf("printf token-%06d", index)
						stamp := canonicalTimestamp(at.Add(time.Duration(index) * time.Second))
						if _, err := statement.ExecContext(ctx, fmt.Sprintf("native:%d", index), command, command,
							stamp, stamp, stamp, exit, state, index+1); err != nil {
							b.Fatal(err)
						}
					}
					if err := tx.Commit(); err != nil {
						b.Fatal(err)
					}
					var build time.Duration
					if indexed {
						started := time.Now()
						if _, err := store.db.ExecContext(ctx, `CREATE INDEX benchmark_native_order
ON history_events(session_id, history_order, event_key) WHERE imported = 0`); err != nil {
							b.Fatal(err)
						}
						build = time.Since(started)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for index := range b.N {
						if operation == "submission" {
							if err := store.RecordHistorySubmission(ctx, HistoryEvent{EventKey: fmt.Sprintf("new:%d", index),
								Command: "pwd", Source: "vuja", SessionID: "new-session", Cwd: "/bench", StartedAt: at}); err != nil {
								b.Fatal(err)
							}
						} else {
							if changed, err := store.CompleteHistoryEvent(ctx, HistoryEvent{EventKey: fmt.Sprintf("native:%d", size/2),
								HasExitCode: true, CompletedAt: at.Add(24 * time.Hour)}); err != nil || !changed {
								b.Fatalf("actual delayed completion: changed=%t error=%v", changed, err)
							}
							if index+1 < b.N {
								b.StopTimer()
								_, err := store.db.ExecContext(ctx, `UPDATE history_events SET state = 'interrupted', exit_code = NULL WHERE event_key = ?`, fmt.Sprintf("native:%d", size/2))
								if err != nil {
									b.Fatal(err)
								}
								b.StartTimer()
							}
						}
					}
					b.StopTimer()
					if operation == "late-completion" {
						for _, previous := range []int{size/2 - 1, size / 2} {
							rows, _ := store.QueryExactTransitionsWithFallback(ctx, fmt.Sprintf("printf token-%06d", previous), "/bench")
							if len(rows) != 1 || rows[0].NextCommand != fmt.Sprintf("printf token-%06d", previous+1) || rows[0].Count != 1 {
								b.Fatalf("portable order changed during measurement: %+v", rows)
							}
						}
					} else if rows, err := store.QueryAllHistoryEvents(ctx); err != nil || len(rows) != size+b.N {
						b.Fatal("measured admission lost an execution")
					}
					var pages, free, pageSize int
					for query, target := range map[string]*int{"PRAGMA page_count": &pages, "PRAGMA freelist_count": &free, "PRAGMA page_size": &pageSize} {
						if err := store.db.QueryRowContext(ctx, query).Scan(target); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64((pages-free)*pageSize), "logical-db-bytes")
					if indexed {
						b.ReportMetric(float64(build.Microseconds()), "index-build-us")
					}
				})
			}
		}
	}
}

// Management-command cost, not an input/render latency assertion. Measure the
// preservation probe and real merge/rebuild together on a populated store.
func BenchmarkHistoryArchiveMerge(b *testing.B) {
	for _, size := range []int{1_000, 10_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
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
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			stamp := canonicalTimestamp(at)
			if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sequence(n) AS (
SELECT 1 UNION ALL SELECT n + 1 FROM sequence WHERE n < ?)
INSERT INTO history_events
(event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
 duration_ns, exit_code, source, session_id, state, history_order, occurrences, imported)
SELECT printf('seed:%d', n), printf('printf token-%06d', n), printf('printf token-%06d', n),
 '/repo', ?, ?, ?, 0, 0, 'vuja', 'bench-session', 'completed', n, 1, 0
FROM sequence`, size, stamp, stamp, stamp); err != nil {
				b.Fatal(err)
			}
			if _, err := advanceNativeHistoryOrder(ctx, tx, size); err != nil {
				b.Fatal(err)
			}
			if err := rebuildHistoryProjectionsAfterRemoval(ctx, tx); err != nil {
				b.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				event := archiveEventForTest(fmt.Sprintf("archive:%d", index), "git fetch", "atuin", true, at)
				if added, err := store.ImportHistoryArchiveEvents(ctx, []HistoryEvent{event}); err != nil || added != 1 {
					b.Fatalf("actual preserved archive merge: %d %v", added, err)
				}
			}
			b.StopTimer()
			if events, err := store.QueryAllHistoryEvents(ctx); err != nil || len(events) != size+b.N {
				b.Fatal("measured archive merge lost canonical executions")
			}
			if edge, _ := store.QueryExactTransitionsWithFallback(ctx, "printf token-000001", "/repo"); len(edge) != 1 || edge[0].NextCommand != "printf token-000002" || edge[0].Count != 1 {
				b.Fatalf("measured archive merge changed existing learning: %+v", edge)
			}
		})
	}
}

// QA3/8: measure the shared retention owner, not a probe-only approximation.
// No-work is a control for the same populated projections. Use -benchtime=1x
// so each reported fixture contains size+1 native executions before pruning.
func BenchmarkHistoryLearningRetention(b *testing.B) {
	for _, size := range []int{1_000, 10_000} {
		for _, operation := range []string{"no-work", "prune-one", "legacy-seconds", "legacy-normalized"} {
			b.Run(fmt.Sprintf("%d/%s", size, operation), func(b *testing.B) {
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
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				keyFormat, session := "seed:%d", "bench-session"
				legacy := operation == "legacy-seconds" || operation == "legacy-normalized"
				if legacy {
					at = at.Add(123456789 * time.Nanosecond)
					keyFormat, session = fmt.Sprintf("vuja:compat:1:%d:%%d", at.UnixNano()), ""
				}
				stamp := canonicalTimestamp(at)
				if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sequence(n) AS (
SELECT 1 UNION ALL SELECT n + 1 FROM sequence WHERE n < ?)
INSERT INTO history_events
(event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
 duration_ns, exit_code, source, session_id, state, history_order, occurrences, imported)
SELECT printf(?, n), printf('printf token-%06d', n), printf('printf token-%06d', n),
 '/repo', ?, ?, ?, 0, 0, 'vuja', ?, 'completed', n, 1, 0
FROM sequence`, size+b.N, keyFormat, stamp, stamp, stamp, session); err != nil {
					b.Fatal(err)
				}
				if _, err := advanceNativeHistoryOrder(ctx, tx, size+b.N); err != nil {
					b.Fatal(err)
				}
				if err := rebuildHistoryProjectionsAfterRemoval(ctx, tx); err != nil {
					b.Fatal(err)
				}
				if operation == "legacy-seconds" {
					for _, table := range []string{"history_entries", "command_outcomes", "argument_values"} {
						if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET last_used = ?", canonicalTimestamp(at.Truncate(time.Second))); err != nil {
							b.Fatal(err)
						}
					}
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for index := range b.N {
					limit, wantRemoved := size+b.N, int64(0)
					if operation != "no-work" {
						limit, wantRemoved = size+b.N-index-1, 1
					}
					if removed, err := store.PruneHistoryEvents(ctx, limit); err != nil || removed != wantRemoved {
						b.Fatalf("actual retention: removed=%d want=%d error=%v", removed, wantRemoved, err)
					}
				}
				b.StopTimer()
				wantCount := size + b.N
				if operation != "no-work" {
					wantCount = size
				}
				if events, err := store.QueryAllHistoryEvents(ctx); err != nil || len(events) != wantCount {
					b.Fatal("measured retention lost an unexpected execution")
				}
				previous := fmt.Sprintf("printf token-%06d", size+b.N-1)
				edge, _ := store.QueryExactTransitionsWithFallback(ctx, previous, "/repo")
				if legacy && len(edge) != 0 || !legacy && (len(edge) != 1 || edge[0].NextCommand != fmt.Sprintf("printf token-%06d", size+b.N) || edge[0].Count != 1) {
					b.Fatalf("measured retention changed a surviving transition: %+v", edge)
				}
			})
		}
	}
}

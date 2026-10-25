package scoring

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// QA3/8: unrelated compatibility learning must survive every removal owner,
// including automatic retention; a no-work policy must remain a cheap no-op.
func TestHistoryLearningRemovalCannotResetUnattributedTransitions(t *testing.T) {
	for _, operation := range []string{"count", "age", "forget", "no-work"} {
		t.Run(operation, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			first := completedForgetFixture(t, store, "native:first", "pwd", at)
			completedForgetFixture(t, store, "native:second", "git status", at.Add(time.Hour))
			if err := store.RecordTransition(t.Context(), "git checkout", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordExactTransition(t.Context(), "git checkout feature", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			exactBefore, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonBefore, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactBefore) != 1 || len(skeletonBefore) != 1 {
				t.Fatal("compatibility learning was not seeded")
			}
			before, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(before) != 2 {
				t.Fatalf("canonical fixture: %+v %v", before, err)
			}
			changesBefore, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var removed int64
			switch operation {
			case "count":
				removed, err = store.PruneHistoryEvents(t.Context(), 1)
			case "age":
				removed, err = store.PruneHistoryEventsWithRetention(t.Context(), 0, at.Add(time.Minute))
			case "forget":
				removed, err = store.ForgetHistoryEvents(t.Context(), []string{first.EventKey})
			case "no-work":
				removed, err = store.PruneHistoryEvents(t.Context(), 10)
			}
			if removed != 0 || operation == "no-work" && err != nil || operation != "no-work" && !errors.Is(err, ErrHistoryUnattributedLearning) {
				t.Fatalf("unsafe removal was not refused or no-work policy changed: removed=%d error=%v", removed, err)
			}
			exactAfter, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonAfter, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactAfter) != 1 || exactAfter[0] != exactBefore[0] || len(skeletonAfter) != 1 || skeletonAfter[0] != skeletonBefore[0] {
				t.Fatalf("removal reset unrelated learning: exact=%+v skeleton=%+v", exactAfter, skeletonAfter)
			}
			after, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(after) != len(before) || after[0] != before[0] || after[1] != before[1] {
				t.Fatalf("refused removal changed canonical executions: %+v %v", after, err)
			}
			if forgotten, err := store.HistoryEventForgotten(t.Context(), first.EventKey); err != nil || forgotten {
				t.Fatalf("refused removal committed a privacy exclusion: %t %v", forgotten, err)
			}
			changesAfter, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil || changesAfter.Latest != changesBefore.Latest {
				t.Fatal("refused/no-work removal published a canonical generation")
			}
		})
	}
}

func TestHistoryLearningCounterpartRemovalCannotResetUnattributedTransitions(t *testing.T) {
	for _, operation := range []string{"native-replay", "pending-forget"} {
		t.Run(operation, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Now().UTC()
			imported := archiveEventForTest("atuin:counterpart", "pwd", "atuin", true, at)
			if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{imported}); err != nil {
				t.Fatal(err)
			}
			if operation == "native-replay" {
				if removed, err := store.ForgetHistoryEvents(t.Context(), []string{imported.EventKey}); err != nil || removed != 1 {
					t.Fatalf("seed imported exclusion: removed=%d error=%v", removed, err)
				}
				// The supported compatibility writer can arrive without using
				// the newer admission path. Model a submission predating the
				// exclusion with an explicit stored clock boundary, not a sleep.
				if err := store.Record(t.Context(), imported.Command, imported.Cwd, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(t.Context(), "UPDATE forgotten_native_history SET forgotten_at = ?", canonicalTimestamp(at.Add(time.Minute))); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(before) != 1 {
				t.Fatalf("counterpart fixture: %+v %v", before, err)
			}
			if err := store.RecordTransition(t.Context(), "git checkout", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordExactTransition(t.Context(), "git checkout feature", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			exactBefore, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonBefore, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactBefore) != 1 || len(skeletonBefore) != 1 {
				t.Fatal("compatibility learning was not seeded")
			}
			if operation == "native-replay" {
				err = store.RecordHistorySubmission(t.Context(), before[0])
			} else {
				pending := HistoryEvent{EventKey: "native:journal-only", Command: imported.Command, NormalizedCommand: imported.Command,
					Cwd: imported.Cwd, Source: "vuja", SessionID: "pending", SubmittedAt: at, StartedAt: at, State: "submitted", Occurrences: 1}
				_, err = store.ForgetConfirmedHistoryEvents(t.Context(), []HistoryEvent{pending}, []HistoryEvent{pending})
			}
			if !errors.Is(err, ErrHistoryUnattributedLearning) {
				t.Fatalf("unsafe counterpart removal was not refused: %v", err)
			}
			after, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(after) != 1 || after[0] != before[0] {
				t.Fatalf("refused counterpart removal changed canonical history: %+v %v", after, err)
			}
			exactAfter, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonAfter, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactAfter) != 1 || exactAfter[0] != exactBefore[0] || len(skeletonAfter) != 1 || skeletonAfter[0] != skeletonBefore[0] {
				t.Fatalf("counterpart removal reset unrelated learning: exact=%+v skeleton=%+v", exactAfter, skeletonAfter)
			}
		})
	}
}

func TestHistoryLearningRecognizesOnlyExplainedLegacyProjectionStates(t *testing.T) {
	for _, fixture := range []string{"modern-only", "legacy-newer", "unattributed-transition", "excess-outcomes"} {
		t.Run(fixture, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			legacyAt, modernAt := at, at.Add(time.Hour)
			if fixture == "legacy-newer" {
				legacyAt, modernAt = modernAt, legacyAt
			}
			old, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, seedErr := old.ExecContext(t.Context(), `CREATE TABLE history_entries
(cmd TEXT NOT NULL, cwd TEXT NOT NULL, count INTEGER, last_used TEXT, UNIQUE(cmd, cwd));
INSERT INTO history_entries VALUES ('git status', '/repo', 3, ?)`, canonicalTimestamp(legacyAt))
			closeErr := old.Close()
			if seedErr != nil || closeErr != nil {
				t.Fatalf("legacy fixture: %v %v", seedErr, closeErr)
			}
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			modern := completedForgetFixture(t, store, "native:modern", "git status", modernAt)
			if fixture == "unattributed-transition" {
				if err := store.RecordExactTransition(t.Context(), "git checkout feature", "npm run dev", "/repo", 0); err != nil {
					t.Fatal(err)
				}
			}
			if fixture == "excess-outcomes" {
				if _, err := store.db.ExecContext(t.Context(), "UPDATE command_outcomes SET successes = 100"); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := store.ForgetHistoryEvents(t.Context(), []string{modern.EventKey})
			refused := fixture == "unattributed-transition" || fixture == "excess-outcomes"
			if refused {
				if removed != 0 || !errors.Is(err, ErrHistoryUnattributedLearning) {
					t.Fatalf("legacy fallback accepted unexplained learning: removed=%d error=%v", removed, err)
				}
			} else if removed != 1 || err != nil {
				t.Fatalf("known incomplete migration projection was not repaired: removed=%d error=%v", removed, err)
			}
			rows, queryErr := store.QueryAllHistoryEvents(t.Context())
			wantRows := 1
			if refused {
				wantRows = 2
			}
			if queryErr != nil || len(rows) != wantRows {
				t.Fatalf("legacy probe leaked canonical mutations: %+v %v", rows, queryErr)
			}
			var legacyCount int
			for _, row := range rows {
				if row.Source == "legacy-vuja" && row.SessionID == "legacy-migration" {
					legacyCount += row.Occurrences
				}
			}
			if legacyCount != 3 {
				t.Fatalf("rolled-back probe lost canonical migration evidence: %+v", rows)
			}
			if fixture == "unattributed-transition" {
				transitions, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
				if len(transitions) != 1 || transitions[0].Count != 1 {
					t.Fatalf("legacy fallback erased unrelated transition evidence: %+v", transitions)
				}
			}
			outcomes, queryErr := store.QueryOutcomes(t.Context(), "/repo", "", "git status", 10)
			wantSuccesses := 3
			switch fixture {
			case "unattributed-transition":
				wantSuccesses = 1
			case "excess-outcomes":
				wantSuccesses = 100
			}
			if queryErr != nil || len(outcomes) != 1 || outcomes[0].Successes != wantSuccesses || outcomes[0].Failures != 0 {
				t.Fatalf("legacy outcome evidence changed incorrectly: %+v %v", outcomes, queryErr)
			}
		})
	}
}

// QA3/8: rolling-window decay changes cached ranking, not attribution. Exercise
// real import/removal owners without an intervening directory refresh.
func TestHistoryLearningPreservesAttributedNavigationAcrossWindowExpiry(t *testing.T) {
	for _, operation := range []string{"import", "forget", "retention", "age"} {
		for _, fixture := range []string{"recent", "expired", "unexplained-count"} {
			t.Run(operation+"/"+fixture, func(t *testing.T) {
				store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				now := time.Now().UTC()
				at := now.Add(-DirectoryRecentWindow - time.Hour)
				projectedAt := now.Add(-2 * time.Hour)
				if fixture == "recent" {
					at, projectedAt = now.Add(-time.Hour), now.Add(-30*time.Minute)
				}
				other := completedForgetFixture(t, store, "native:unrelated", "pwd", at.Add(-time.Hour))
				navigation := completedForgetFixture(t, store, "native:visit", "z project", at)
				if err := store.recordHistoryDirectoryAt(t.Context(), navigation.EventKey, "/repo/project", projectedAt); err != nil {
					t.Fatal(err)
				}
				var recent int
				if err := store.db.QueryRowContext(t.Context(), "SELECT recent_count FROM directory_navigation_sources WHERE source = 'native-visits' AND path = '/repo/project'").Scan(&recent); err != nil || recent != 1 {
					t.Fatalf("visit was not initially inside the recorded projection window: recent=%d error=%v", recent, err)
				}
				if fixture == "unexplained-count" {
					if _, err := store.db.ExecContext(t.Context(), "UPDATE directory_navigation_sources SET count = count + 1 WHERE source = 'native-visits'"); err != nil {
						t.Fatal(err)
					}
				}
				before, err := store.QueryHistoryChanges(t.Context(), 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				wantRecords := 1
				switch operation {
				case "import":
					fresh := archiveEventForTest("zsh:unrelated", "git fetch", "zsh", true, now)
					_, err = store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{fresh})
					wantRecords = 3
				case "forget":
					_, err = store.ForgetHistoryEvents(t.Context(), []string{other.EventKey})
				case "retention":
					_, err = store.PruneHistoryEvents(t.Context(), 1)
				case "age":
					_, err = store.PruneHistoryEventsWithRetention(t.Context(), 0, at)
				}
				if fixture == "unexplained-count" {
					if !errors.Is(err, ErrHistoryUnattributedLearning) {
						t.Fatalf("known time decay waived unexplained lifetime counts: %v", err)
					}
					wantRecords = 2
				} else if err != nil {
					t.Fatalf("fully attributed history was refused after known window decay: %v", err)
				}
				events, queryErr := store.QueryAllHistoryEvents(t.Context())
				if queryErr != nil || len(events) != wantRecords {
					t.Fatalf("wrong canonical result: records=%d want=%d error=%v", len(events), wantRecords, queryErr)
				}
				var count, recentAfter int
				if err := store.db.QueryRowContext(t.Context(), "SELECT count, recent_count FROM directory_navigation_sources WHERE source = 'native-visits' AND path = '/repo/project'").Scan(&count, &recentAfter); err != nil {
					t.Fatal(err)
				}
				wantCount, wantRecent := 1, 0
				switch fixture {
				case "recent":
					wantRecent = 1
				case "unexplained-count":
					wantCount, wantRecent = 2, 1
					after, err := store.QueryHistoryChanges(t.Context(), 0, 100)
					if err != nil || after.Latest != before.Latest {
						t.Fatal("refused removal published changes")
					}
				}
				if count != wantCount || recentAfter != wantRecent {
					t.Fatalf("navigation attribution/recency changed incorrectly: count=%d recent=%d want=%d/%d", count, recentAfter, wantCount, wantRecent)
				}
			})
		}
	}
}

// QA3/8: exercise both the supported compatibility writer and its persisted
// historical second-resolution representation, not export alone.
func TestHistoryLearningPreservesCompatibleRecordWriters(t *testing.T) {
	for _, operation := range []string{"import", "forget", "retention", "age"} {
		for _, fixture := range []string{"current", "legacy-seconds", "legacy-next-second"} {
			for _, exit := range []int{0, 7} {
				for _, reopen := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/exit=%d/reopen=%t", operation, fixture, exit, reopen), func(t *testing.T) {
						path := filepath.Join(t.TempDir(), "history.db")
						store, err := NewFrecencyStore(path)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = store.Close() })
						other := completedForgetFixture(t, store, "native:other", "pwd", time.Now().Add(-24*time.Hour))
						const command = "printf compatibility-token"
						if err := store.Record(t.Context(), command, "/repo", exit); err != nil {
							t.Fatal(err)
						}
						events, err := store.QueryAllHistoryEvents(t.Context())
						if err != nil || len(events) != 2 {
							t.Fatalf("record fixture: %+v %v", events, err)
						}
						recorded := events[0]
						if recorded.Command != command {
							t.Fatal("recorded execution was not the latest")
						}
						if fixture != "current" {
							legacyKey := fmt.Sprintf("vuja:compat:1:%d:1", recorded.CompletedAt.UnixNano())
							if _, err := store.db.ExecContext(t.Context(), "UPDATE history_events SET event_key = ? WHERE event_key = ?", legacyKey, recorded.EventKey); err != nil {
								t.Fatal(err)
							}
							coarse := recorded.CompletedAt.Truncate(time.Second)
							if fixture == "legacy-next-second" {
								coarse = coarse.Add(time.Second)
							}
							for _, table := range []string{"history_entries", "command_outcomes", "argument_values", "recent_failures"} {
								column, predicate := "last_used", "cmd = ?"
								value := command
								switch table {
								case "argument_values":
									predicate, value = "value = ?", "compatibility-token"
								case "recent_failures":
									column = "failed_at"
								}
								if _, err := store.db.ExecContext(t.Context(), "UPDATE "+table+" SET "+column+" = ? WHERE "+predicate, canonicalTimestamp(coarse), value); err != nil {
									t.Fatal(err)
								}
							}
						}
						if reopen {
							if err := store.Close(); err != nil {
								t.Fatal(err)
							}
							store, err = NewFrecencyStore(path)
							if err != nil {
								t.Fatal(err)
							}
						}
						wantCount := 1
						switch operation {
						case "import":
							_, err = store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{archiveEventForTest("zsh:fresh", "git fetch", "zsh", true, time.Now())})
							wantCount = 3
						case "forget":
							_, err = store.ForgetHistoryEvents(t.Context(), []string{other.EventKey})
						case "retention":
							_, err = store.PruneHistoryEvents(t.Context(), 1)
						case "age":
							_, err = store.PruneHistoryEventsWithRetention(t.Context(), 0, recorded.StartedAt.Add(-time.Hour))
						}
						if err != nil {
							t.Fatalf("supported writer was misclassified as unattributed learning: %v", err)
						}
						if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != wantCount {
							t.Fatalf("wrong surviving execution count: %d want=%d error=%v", len(events), wantCount, err)
						}
						if entries, err := store.QueryLocal(t.Context(), "/repo", command, 10); err != nil || len(entries) != 1 || entries[0].Count != 1 {
							t.Fatalf("compatibility frequency changed: %+v %v", entries, err)
						}
						var successes, failures int
						if err := store.db.QueryRowContext(t.Context(), "SELECT successes, failures FROM command_outcomes WHERE cmd = ? AND cwd = '/repo'", command).Scan(&successes, &failures); err != nil || successes+failures != 1 || (exit == 0) != (successes == 1) {
							t.Fatalf("compatibility outcome changed: %d/%d %v", successes, failures, err)
						}
					})
				}
			}
		}
	}
}

func TestHistoryLearningKeepsModernTimestampDifferencesStrict(t *testing.T) {
	for _, table := range []string{"history_entries", "command_outcomes", "argument_values", "recent_failures"} {
		for _, change := range []string{"fractional", "whole-second"} {
			t.Run(table+"/"+change, func(t *testing.T) {
				store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				const command = "printf precise-token"
				exit := 0
				if table == "recent_failures" {
					exit = 7
				}
				if err := store.Record(t.Context(), command, "/repo", exit); err != nil {
					t.Fatal(err)
				}
				events, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != 1 {
					t.Fatalf("modern record fixture: %+v %v", events, err)
				}
				// A different owned legacy row can share the same instant. Its
				// coarse clock is not permission to relax this modern row.
				at := events[0].CompletedAt
				legacy := HistoryEvent{EventKey: fmt.Sprintf("vuja:compat:1:%d:1", at.UnixNano()), Command: "echo other", NormalizedCommand: "echo other", Cwd: "/other", Source: "vuja", SubmittedAt: at, StartedAt: at, CompletedAt: at, State: "completed", HasExitCode: true, Occurrences: 1}
				if err := store.RecordHistoryEvent(t.Context(), legacy); err != nil {
					t.Fatal(err)
				}
				tx, err := store.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := rebuildHistoryProjectionsAfterRemoval(t.Context(), tx); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				changed := events[0].CompletedAt.Add(123 * time.Nanosecond)
				if change == "whole-second" {
					changed = events[0].CompletedAt.Truncate(time.Second).Add(time.Second)
				}
				column := "last_used"
				if table == "recent_failures" {
					column = "failed_at"
				}
				if _, err := store.db.ExecContext(t.Context(), "UPDATE "+table+" SET "+column+" = ? WHERE cwd = '/repo'", canonicalTimestamp(changed)); err != nil {
					t.Fatal(err)
				}
				before, err := store.QueryHistoryChanges(t.Context(), 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				fresh := archiveEventForTest("zsh:unrelated", "git fetch", "zsh", true, time.Now())
				if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{fresh}); added != 0 || !errors.Is(err, ErrHistoryUnattributedLearning) {
					t.Fatalf("modern precision mismatch was waived: added=%d error=%v", added, err)
				}
				after, err := store.QueryHistoryChanges(t.Context(), 0, 100)
				if err != nil || after.Latest != before.Latest {
					t.Fatal("refused modern mismatch published changes")
				}
			})
		}
	}
}

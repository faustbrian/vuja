package scoring

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryForgetImportedConsumesOneNativeIdentityAcrossMigrationCohorts(t *testing.T) {
	for _, legacyClosest := range []bool{true, false} {
		name := "current-closest"
		if legacyClosest {
			name = "legacy-closest"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			legacyAt, currentAt := at.Add(100*time.Millisecond), at
			if legacyClosest {
				legacyAt, currentAt = at, at.Add(100*time.Millisecond)
			}
			old, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, seedErr := old.ExecContext(t.Context(), `CREATE TABLE history_entries (
                cmd TEXT NOT NULL, cwd TEXT NOT NULL, count INTEGER,
                last_used TEXT NOT NULL, UNIQUE(cmd, cwd));
                INSERT INTO history_entries VALUES ('ssh forge@one', '/repo', 1, ?)`, canonicalTimestamp(legacyAt))
			closeErr := old.Close()
			if seedErr != nil || closeErr != nil {
				t.Fatalf("pre-upgrade fixture: %v %v", seedErr, closeErr)
			}
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			migrated, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(migrated) != 1 || migrated[0].Source != "legacy-vuja" || migrated[0].Occurrences != 1 {
				t.Fatalf("real one-occurrence native migration: %+v %v", migrated, err)
			}
			current := completedForgetFixture(t, store, "native:current", "ssh forge@one", currentAt)
			imported := HistoryEvent{EventKey: "atuin:target", Command: current.Command, Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, HasExitCode: true, State: "completed"}
			if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{imported}); err != nil {
				t.Fatal(err)
			}
			forgotten, survivor := current.EventKey, migrated[0].EventKey
			if legacyClosest {
				forgotten, survivor = survivor, forgotten
			}
			if removed, err := store.ForgetHistoryEvents(t.Context(), []string{imported.EventKey}); err != nil || removed != 1 {
				t.Fatalf("one imported target: removed=%d error=%v", removed, err)
			}
			assertSurvivor := func() {
				t.Helper()
				events, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != 1 || events[0].EventKey != survivor {
					t.Fatalf("one imported slot consumed multiple native cohorts: %+v %v", events, err)
				}
				var bound string
				if err := store.db.QueryRowContext(t.Context(), `SELECT native_identity_hash FROM forgotten_native_history WHERE identity_hash = ?`, forgottenHistoryIdentity(imported.EventKey)).Scan(&bound); err != nil || bound != forgottenHistoryIdentity(forgotten) {
					t.Fatalf("native slot must bind only the closest identity: %v", err)
				}
				if excluded, err := store.HistoryEventForgotten(t.Context(), survivor); err != nil || excluded {
					t.Fatalf("independent native repetition was excluded: %v %v", excluded, err)
				}
				if entries, err := store.QueryGlobal(t.Context(), current.Command, 10); err != nil || len(entries) != 1 || entries[0].Count != 1 {
					t.Fatalf("surviving native ranking count: %+v %v", entries, err)
				}
			}
			assertSurvivor()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			assertSurvivor()
		})
	}
}

// Goal QA3: forgetting identifies the execution, not just the adapter row.
func TestHistoryForgetImportedRemovesExistingNativeCounterpartOnly(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{EventKey: "atuin:original", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	repeat := original
	repeat.EventKey, repeat.StartedAt = "atuin:repeat", at.Add(200*time.Millisecond)
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{original, repeat}); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"native:original", "native:repeat"} {
		completedForgetFixture(t, store, id, original.Command, at.Add(time.Duration(index)*200*time.Millisecond))
		if err := store.RecordExecutionFeedback(t.Context(), id, original.Command, "/repo", "typed"); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{original.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget imported execution: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 2 {
		t.Fatalf("native representation survived imported forgetting: %+v %v", events, err)
	}
	for _, event := range events {
		if event.EventKey != repeat.EventKey && event.EventKey != "native:repeat" {
			t.Fatalf("wrong surviving execution: %+v", event)
		}
	}
	if feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "ssh", 10); err != nil || len(feedback) != 1 || feedback[0].Typed != 1 {
		t.Fatalf("forgotten native feedback remains: %+v %v", feedback, err)
	}
	if forgotten, err := store.HistoryEventForgotten(t.Context(), "native:original"); err != nil || !forgotten {
		t.Fatalf("native alias must remain excluded for delayed publishers/replay: %v %v", forgotten, err)
	}
}

func TestHistoryForgetImportedConsumesOnlyOneDelayedNativeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{EventKey: "atuin:original", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{original}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{original.EventKey}); err != nil {
		t.Fatal(err)
	}
	native := original
	native.EventKey, native.Source, native.Imported = "native:delayed", "vuja", false
	if err := store.RecordHistorySubmission(t.Context(), native); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("delayed native counterpart must be excluded: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.RecordHistorySubmission(t.Context(), native); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("native alias exclusion did not survive reopen: %v", err)
	}
	// No external row is needed to prove that a second native ID is a separate
	// execution. A consumed imported slot cannot eat another nearby submission.
	repeat := native
	repeat.EventKey, repeat.StartedAt = "native:near-repeat", at.Add(200*time.Millisecond)
	if err := reopened.RecordHistorySubmission(t.Context(), repeat); err != nil {
		t.Fatalf("consumed slot swallowed an independent nearby repeat: %v", err)
	}
	if events, err := reopened.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != repeat.EventKey {
		t.Fatalf("wrong native identities after replay/repetition: %+v %v", events, err)
	}
}

func TestHistoryForgetImportedPreservesStrongerDelayedNativeRepeat(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{EventKey: "atuin:original", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	repeat := original
	repeat.EventKey, repeat.StartedAt = "atuin:repeat", at.Add(200*time.Millisecond)
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{original, repeat}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{original.EventKey}); err != nil {
		t.Fatal(err)
	}
	native := repeat
	native.EventKey, native.Source, native.Imported = "native:repeat-first", "vuja", false
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatalf("stronger surviving repetition must reserve its own native copy: %v", err)
	}
	copy := original
	copy.EventKey, copy.Source, copy.Imported = "native:original-late", "vuja", false
	if err := store.RecordHistorySubmission(t.Context(), copy); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("late original copy bypassed the available imported slot: %v", err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatalf("wrong surviving imported/native representations: %+v %v", events, err)
	}
}

func TestHistoryForgetImportedCannotConsumeAPostForgetSubmission(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{EventKey: "atuin:original", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{original}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{original.EventKey}); err != nil {
		t.Fatal(err)
	}
	// A deterministic stored clock boundary, not a timing assertion. The new
	// execution is within the matching window but was submitted after deletion.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE forgotten_native_history SET forgotten_at = ?`, canonicalTimestamp(at.Add(50*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	native := original
	native.EventKey, native.Source, native.Imported = "native:new", "vuja", false
	native.SubmittedAt, native.StartedAt = at.Add(100*time.Millisecond), at.Add(100*time.Millisecond)
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatalf("a future native execution is not an old imported counterpart: %v", err)
	}
}

func TestHistoryForgetCompactImportCannotExpandExcludedOccurrenceScope(t *testing.T) {
	for _, variant := range []string{"new-counterpart", "observed-alias", "explicit-compact", "legacy-exclusion"} {
		t.Run(variant, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			copy := HistoryEvent{EventKey: "atuin:copy", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
			if variant == "explicit-compact" {
				copy.Occurrences = 7
				if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ForgetHistoryEvents(t.Context(), []string{copy.EventKey}); err != nil {
					t.Fatal(err)
				}
			} else {
				completedForgetFixture(t, store, "native:original", copy.Command, at)
				if variant == "observed-alias" {
					if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.ForgetHistoryEvents(t.Context(), []string{"native:original"}); err != nil {
					t.Fatal(err)
				}
				copy.Occurrences = 7
			}
			if variant == "legacy-exclusion" {
				// Older exclusion rows retain their single-occurrence authority
				// when upgraded; absence of metadata must not authorize a group.
				if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE forgotten_history_events DROP COLUMN occurrences`); err != nil {
					t.Fatal(err)
				}
				copy.EventKey = "native:original"
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			err = store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{copy})
			if variant == "explicit-compact" {
				if err != nil {
					t.Fatalf("reimporting the explicitly forgotten whole group must stay excluded: %v", err)
				}
			} else if err == nil {
				t.Fatal("import silently discarded seven occurrences under a one-occurrence exclusion")
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
				t.Fatalf("refused import or explicit exclusion resurrected history: %+v %v", events, err)
			}
		})
	}
}

func TestHistoryForgetImportedNativeCounterpartRefusalRollsBackWholeSelection(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{EventKey: "atuin:original", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{original}); err != nil {
		t.Fatal(err)
	}
	completedForgetFixture(t, store, "native:original", original.Command, at)
	if err := store.RecordFeedback(t.Context(), original.Command, "/repo", "typed"); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{original.EventKey}); removed != 0 || !errors.Is(err, ErrHistoryUnattributedLearning) {
		t.Fatalf("imported selection bypassed native learning refusal: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatalf("refused counterpart removal changed canonical data: %+v %v", events, err)
	}
	for _, id := range []string{original.EventKey, "native:original"} {
		if forgotten, err := store.HistoryEventForgotten(t.Context(), id); err != nil || forgotten {
			t.Fatalf("refused selection committed an exclusion for %s: %v %v", id, forgotten, err)
		}
	}
}

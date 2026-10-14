package scoring

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryFeedbackUnrelatedLegacyLearningSurvivesRetention(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordFeedback(t.Context(), "git diff", "/repo", "dismissed"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	completedForgetFixture(t, store, "old", "ssh forge@old", at)
	completedForgetFixture(t, store, "new", "git status", at.Add(time.Hour))
	if removed, err := store.PruneHistoryEvents(t.Context(), 1); err != nil || removed != 1 {
		t.Fatalf("retention: removed=%d error=%v", removed, err)
	}
	if feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "git diff", 10); err != nil || len(feedback) != 1 || feedback[0].Dismissed != 1 {
		t.Fatalf("retention erased unrelated legacy learning: %+v %v", feedback, err)
	}
}

func TestHistoryFeedbackUpgradeDoesNotRetainOrRestoreRejectedLearning(t *testing.T) {
	for _, schema := range []string{"pre-lifecycle", "lifecycle-v2"} {
		t.Run(schema, func(t *testing.T) {
			testHistoryFeedbackUpgradePolicy(t, schema == "lifecycle-v2")
		})
	}
}

func testHistoryFeedbackUpgradePolicy(t *testing.T, lifecycleV2 bool) {
	path := filepath.Join(t.TempDir(), "history.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, seedErr := old.ExecContext(t.Context(), `CREATE TABLE suggestion_feedback (
        cmd TEXT NOT NULL, cwd TEXT NOT NULL, accepted INTEGER DEFAULT 0,
        typed INTEGER DEFAULT 0, edited INTEGER DEFAULT 0, dismissed INTEGER DEFAULT 0,
        last_used TEXT, UNIQUE(cmd, cwd));
        INSERT INTO suggestion_feedback (cmd, cwd, typed, last_used) VALUES
        ('git diff', '/repo', 3, '2026-10-01T12:00:00Z'),
        ('curl --password fictional-value', '/repo', 1, '2026-10-01T12:00:00Z'),
        (' ignored-command', '/repo', 1, '2026-10-01T12:00:00Z'),
        ('echo "vuja history forget private-example"', '/repo', 1, '2026-10-01T12:00:00Z');`)
	if seedErr == nil && lifecycleV2 {
		_, seedErr = old.ExecContext(t.Context(), `CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT);
            INSERT INTO metadata (key, value) VALUES ('history_schema_version', '2');`)
	}
	closeErr := old.Close()
	if seedErr != nil || closeErr != nil {
		t.Fatalf("pre-upgrade fixture without attribution marker: %v %v", seedErr, closeErr)
	}
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertPrivateAndUseful := func(phase string) {
		t.Helper()
		for _, table := range []string{"suggestion_feedback", "legacy_suggestion_feedback"} {
			var rejected int
			if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE cmd != 'git diff'").Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if rejected != 0 {
				t.Errorf("%s: %s retains %d policy-rejected learning rows", phase, table, rejected)
			}
		}
		entries, err := store.QueryFeedback(t.Context(), "/repo", "", "git diff", 10)
		if err != nil || len(entries) != 1 || entries[0].Typed != 3 {
			t.Errorf("%s: unrelated accepted legacy learning changed: %+v %v", phase, entries, err)
		}
	}
	assertPrivateAndUseful("upgrade")
	execution := completedForgetFixture(t, store, "unrelated-removal", "git status", time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{execution.EventKey}); err != nil || removed != 1 {
		t.Fatalf("unrelated removal/rebuild: removed=%d error=%v", removed, err)
	}
	assertPrivateAndUseful("projection rebuild")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertPrivateAndUseful("reopen")
}

func TestHistoryFeedbackMigrationPreservesLegacyLearningAcrossReopenAndAttributedForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), `
CREATE TABLE history_entries (id INTEGER PRIMARY KEY, cmd TEXT NOT NULL, cwd TEXT NOT NULL, count INTEGER, last_used TIMESTAMP, UNIQUE(cmd, cwd));
INSERT INTO history_entries VALUES (1, 'git status', '/repo', 3, '2026-10-01 12:00:00');
CREATE TABLE suggestion_feedback (cmd TEXT NOT NULL, cwd TEXT NOT NULL, accepted INTEGER DEFAULT 0, typed INTEGER DEFAULT 0, edited INTEGER DEFAULT 0, dismissed INTEGER DEFAULT 0, last_used TIMESTAMP, UNIQUE(cmd, cwd));
INSERT INTO suggestion_feedback VALUES ('git status', '/repo', 3, 2, 0, 0, '2026-10-01 12:00:00');
`); err != nil {
		_ = old.Close()
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	execution := completedForgetFixture(t, store, "feedback:new", "git status", time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	if err := store.RecordExecutionFeedback(t.Context(), execution.EventKey, "git status", "/repo", "typed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10)
	if err != nil || len(before) != 1 || before[0].Accepted != 3 || before[0].Typed != 3 {
		t.Fatalf("migration/reopen changed independently expected learning: %+v %v", before, err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{execution.EventKey}); err != nil {
		t.Fatal(err)
	}
	after, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10)
	if err != nil || len(after) != 1 || after[0].Accepted != 3 || after[0].Typed != 2 {
		t.Fatalf("attributed forgetting removed unrelated legacy learning: %+v %v", after, err)
	}
}

func TestHistoryFeedbackRetryAndRetentionFollowExecutionIdentity(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"feedback:first", "feedback:second"} {
		completedForgetFixture(t, store, id, "git status", at.Add(time.Duration(index)*time.Minute))
		for retry := 0; retry < 2; retry++ {
			if err := store.RecordExecutionFeedback(t.Context(), id, "git status", "/repo", "typed"); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordExecutionFeedback(t.Context(), id, "git diff", "/repo", "dismissed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10)
	if err != nil || len(before) != 2 {
		t.Fatalf("missing feedback: %+v %v", before, err)
	}
	for _, entry := range before {
		if entry.Typed+entry.Dismissed != 2 {
			t.Fatalf("retry inflated one execution's feedback: %+v", entry)
		}
	}
	if removed, err := store.PruneHistoryEvents(t.Context(), 1); err != nil || removed != 1 {
		t.Fatalf("retention fixture: removed=%d error=%v", removed, err)
	}
	after, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10)
	if err != nil || len(after) != 2 {
		t.Fatalf("retention erased surviving offered feedback: %+v %v", after, err)
	}
	for _, entry := range after {
		if entry.Typed+entry.Dismissed != 1 {
			t.Fatalf("retention retained removed contribution: %+v", entry)
		}
	}
}

func TestHistoryFeedbackProjectionFailureRollsBackItsAttribution(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	execution := completedForgetFixture(t, store, "feedback:failure", "git status", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_feedback BEFORE INSERT ON suggestion_feedback BEGIN SELECT RAISE(ABORT, 'fixture projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionFeedback(t.Context(), execution.EventKey, "git status", "/repo", "typed"); err == nil {
		t.Fatal("failed projection reported success")
	}
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM history_feedback_events WHERE event_key = ?`, execution.EventKey).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed projection left an attribution that would swallow a retry: count=%d error=%v", count, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER fail_feedback`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionFeedback(t.Context(), execution.EventKey, "git status", "/repo", "typed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{execution.EventKey}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionFeedback(t.Context(), execution.EventKey, "git status", "/repo", "typed"); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("late feedback must be a deliberate privacy rejection: %v", err)
	}
}

func TestHistoryFeedbackUnattributedForgetRefusesResetAndRollsBackSelection(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	modern := completedForgetFixture(t, store, "modern", "git diff", at)
	legacy := completedForgetFixture(t, store, "unattributed", "git status", at.Add(time.Minute))
	completedForgetFixture(t, store, "surviving-repeat", "git status", at.Add(2*time.Minute))
	if err := store.RecordExecutionFeedback(t.Context(), modern.EventKey, modern.Command, modern.Cwd, "typed"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := store.RecordFeedback(t.Context(), legacy.Command, legacy.Cwd, "typed"); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{modern.EventKey, legacy.EventKey}); err == nil || removed != 0 {
		t.Fatalf("selective deletion silently reset unattributed learning: removed=%d err=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 3 {
		t.Fatal("refused legacy reset partially deleted canonical executions")
	}
	for _, id := range []string{modern.EventKey, legacy.EventKey} {
		if forgotten, err := store.HistoryEventForgotten(t.Context(), id); err != nil || forgotten {
			t.Fatal("refused legacy reset committed a partial forgetting exclusion")
		}
	}
	feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10)
	if err != nil || len(feedback) != 2 {
		t.Fatal("refused legacy reset removed feedback")
	}
	for _, entry := range feedback {
		want := 1
		if entry.Cmd == "git status" {
			want = 3
		}
		if entry.Typed != want {
			t.Fatalf("refused legacy reset changed surviving feedback: %+v", entry)
		}
	}
}

func TestHistoryConservativeForgettingTextCannotEnterImportsOrLearning(t *testing.T) {
	for _, command := range []string{
		"printf 'vuja history forget --command fixture-command'",
		"! VUJA_MODE=local vuja history forget --id first",
		"time VUJA_MODE=local vuja history forget --id first",
	} {
		t.Run(command, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			event := HistoryEvent{EventKey: "conservative-private", Command: command, Cwd: "/repo", Source: "vuja", StartedAt: time.Now().UTC()}
			if err := store.RecordHistorySubmission(t.Context(), event); err == nil {
				t.Fatal("canonical recording accepted conservatively excluded text")
			}
			if err := store.RecordFeedback(t.Context(), command, "/repo", "typed"); err != nil {
				t.Fatal(err)
			}
			event.Source, event.Imported, event.State = "zsh", true, "completed"
			if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{event}); err != nil {
				t.Fatal(err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
				t.Fatal("import refresh retained conservatively excluded text")
			}
			if feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "", 10); err != nil || len(feedback) != 0 {
				t.Fatal("feedback retained conservatively excluded text")
			}
		})
	}
}

package scoring

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

// QA scenarios 1/8: observe SQLite's actual connection-local policy from a
// trigger in the production write, without a wall-clock pass/fail threshold.
func TestHistoryWritesUseCallerLockBudget(t *testing.T) {
	for _, name := range []string{"submission", "completion", "compatibility", "background", "long-budget"} {
		t.Run(name, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			event := HistoryEvent{EventKey: "budget-entry", Command: "ssh forge@fixture", Cwd: "/fixture", Source: "vuja", StartedAt: time.Now()}
			if name == "completion" {
				if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.db.ExecContext(t.Context(), `
CREATE TABLE observed_write_budget (timeout INTEGER NOT NULL);
CREATE TRIGGER observe_history_insert AFTER INSERT ON history_events BEGIN
    INSERT INTO observed_write_budget SELECT timeout FROM pragma_busy_timeout;
END;
CREATE TRIGGER observe_history_update AFTER UPDATE ON history_events BEGIN
    INSERT INTO observed_write_budget SELECT timeout FROM pragma_busy_timeout;
END;`)
			if err != nil {
				t.Fatal(err)
			}
			budget := time.Second
			if name == "long-budget" {
				budget = 10 * time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			switch name {
			case "completion":
				event.HasExitCode, event.CompletedAt = true, event.StartedAt.Add(time.Second)
				if changed, err := store.CompleteHistoryEvent(ctx, event); err != nil || !changed {
					t.Fatalf("completion: changed=%v err=%v", changed, err)
				}
			case "compatibility":
				if err := store.Record(t.Context(), "git status", "/fixture", 0); err != nil {
					t.Fatal(err)
				}
			case "background":
				if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			default:
				if err := store.RecordHistorySubmission(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			var actual int
			if err := store.db.QueryRowContext(t.Context(), "SELECT timeout FROM observed_write_budget").Scan(&actual); err != nil {
				t.Fatal(err)
			}
			if name == "background" || name == "long-budget" {
				if actual != 5000 {
					t.Fatalf("ordinary write lost its 5000ms policy: %d", actual)
				}
			} else if actual <= 0 || actual > 1000 {
				t.Fatalf("write used %dms SQLite lock wait despite a 1000ms caller budget", actual)
			}
			assertHistoryPoolPolicyForTest(t, store)
		})
	}
}

func TestHistoryWriteBudgetRestoresPolicyAfterWriterContention(t *testing.T) {
	for _, name := range []string{"warm-connection", "fresh-connection"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if name == "fresh-connection" {
				store.db.SetMaxIdleConns(0)
			}
			databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
			blocker, err := sql.Open("sqlite", databaseURL.String())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			connection, err := blocker.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK") }()
			entry := HistoryEvent{EventKey: "blocked-entry", Command: "ssh forge@fixture", Cwd: "/fixture", Source: "vuja", StartedAt: time.Now()}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			started := time.Now()
			err = store.RecordHistorySubmission(ctx, entry)
			cancel()
			t.Logf("canonical write while independent writer stayed locked: %s (error: %v)", time.Since(started), err)
			if err == nil {
				t.Fatal("independent writer lock unexpectedly admitted a canonical write")
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
				t.Fatalf("failed write left partial history: events=%+v error=%v", events, err)
			}
			assertHistoryPoolPolicyForTest(t, store)
			if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordHistorySubmission(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != entry.EventKey {
				t.Fatalf("post-contention write did not succeed exactly once: events=%+v error=%v", events, err)
			}
		})
	}
}

func TestHistoryWriteBudgetRestoresPolicyAfterTransactionFailure(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_fixture BEFORE INSERT ON history_events BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	entry := HistoryEvent{EventKey: "rejected-entry", Command: "git status", Source: "vuja", StartedAt: time.Now()}
	if err := store.RecordHistorySubmission(ctx, entry); err == nil {
		t.Fatal("fixture rejection was ignored")
	}
	assertHistoryPoolPolicyForTest(t, store)
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("failed transaction left partial history: events=%+v error=%v", events, err)
	}
}

func assertHistoryPoolPolicyForTest(t *testing.T, store *FrecencyStore) {
	t.Helper()
	var connections []*sql.Conn
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range 4 {
		connection, err := store.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		var timeout, synchronous int
		if err := connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if timeout != 5000 || synchronous != 2 {
			t.Fatalf("write policy leaked into the reader pool: timeout=%d synchronous=%d", timeout, synchronous)
		}
	}
}

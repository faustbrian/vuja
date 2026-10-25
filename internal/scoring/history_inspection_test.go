package scoring

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectHistoryReadsActiveWALWithoutChangingEventsOrPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := HistoryEvent{EventKey: "one", Command: "private-command", Cwd: "/private", SessionID: "fixture", Source: "vuja", SubmittedAt: time.Now(), State: "submitted"}
	if err := store.RecordHistorySubmission(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		health, stats, err := InspectHistory(context.Background(), path)
		if err != nil || health.QuickCheck != "ok" || stats.Events != 1 || stats.Unfinished != 1 {
			t.Fatalf("read-only inspection missed active WAL data: health=%+v stats=%+v err=%v", health, stats, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatal("inspection repaired database permissions")
	}
	stats, err := store.HistoryStats(context.Background())
	if err != nil || stats.Events != 1 {
		t.Fatal("inspection changed canonical events")
	}
}

func TestInspectHistoryDoesNotCreateOrInitializeDatabases(t *testing.T) {
	root := t.TempDir()
	absent := filepath.Join(root, "missing", "history.db")
	if _, _, err := InspectHistory(context.Background(), absent); !os.IsNotExist(err) {
		t.Fatalf("missing database should remain unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(absent)); !os.IsNotExist(err) {
		t.Fatal("inspection created a missing database directory")
	}
	legacy := filepath.Join(root, "legacy.db")
	db, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE existing_only (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectHistory(context.Background(), legacy); err == nil {
		t.Fatal("uninitialized schema was reported as supported")
	}
	var tables int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("inspection initialized an unsupported database: tables=%d err=%v", tables, err)
	}
}

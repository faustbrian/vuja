package scoring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFrecencyStore_RecordAndQueryLocal(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	cwd := "/home/user/project"
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git commit -m 'test'", cwd, 0)

	entries, err := store.QueryLocal(context.Background(), cwd, "git", 10)
	if err != nil {
		t.Fatalf("QueryLocal failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Cmd != "git status" || entries[0].Count != 3 {
		t.Errorf("expected top entry to be 'git status' with count 3, got %s (count %d)", entries[0].Cmd, entries[0].Count)
	}
}

func TestFrecencyStore_QueryProjectAggregatesOnlyRepositoryDescendants(t *testing.T) {
	root := t.TempDir()
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.Record(ctx, "go test ./...", filepath.Join(root, "service-a"), 0)
	_ = store.Record(ctx, "go test ./...", filepath.Join(root, "service-b"), 0)
	_ = store.Record(ctx, "go test sibling", root+"-sibling", 0)

	entries, err := store.QueryProject(ctx, root, "go test", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Cmd != "go test ./..." || entries[0].Count != 2 {
		t.Fatalf("expected repository-scoped aggregate, got %+v", entries)
	}
}

func TestFrecencyStore_LearnsSuccessfulArgumentValuesBySlot(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "service")
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.Record(ctx, "git switch feature/payments", cwd, 0)
	_ = store.Record(ctx, "git switch feature/payments", cwd, 0)
	_ = store.Record(ctx, "git switch feature/broken", cwd, 1)

	values, err := store.QueryArgumentValues(ctx, cwd, root, "git switch", 2, "feature/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Value != "feature/payments" {
		t.Fatalf("expected successful branch value, got %+v", values)
	}
}

func TestFrecencyStore_TracksOnlyTheLatestUnrecoveredFailure(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cwd := "/repo/service"

	if err := store.Record(ctx, "gti status", cwd, 127); err != nil {
		t.Fatal(err)
	}
	failure, ok := store.QueryRecentFailure(ctx, cwd, 5*time.Minute)
	if !ok || failure.Command != "gti status" || failure.ExitCode != 127 {
		t.Fatalf("expected command-not-found failure, got %+v, %v", failure, ok)
	}

	if err := store.Record(ctx, "git status", cwd, 0); err != nil {
		t.Fatal(err)
	}
	if failure, ok := store.QueryRecentFailure(ctx, cwd, 5*time.Minute); ok {
		t.Fatalf("expected successful command to clear recovery, got %+v", failure)
	}
}

func TestFrecencyStore_ReplacesImportedDirectorySourcesIdempotently(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	entries := []DirectoryImport{
		{Path: "/repo/service", Count: 5, LastUsed: time.Now()},
		{Path: "/repo/other", Count: 2, LastUsed: time.Now().Add(-time.Hour)},
	}
	if replaceErr := store.ReplaceDirectorySource(ctx, "atuin", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if replaceErr := store.ReplaceDirectorySource(ctx, "atuin", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	paths, err := store.QueryDirectories(ctx, "service", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/repo/service" {
		t.Fatalf("expected one idempotent imported directory, got %v", paths)
	}
}

func TestFrecencyStore_RawScoreDistribution(t *testing.T) {
	store := &FrecencyStore{}
	now := time.Now()

	oldHeavyScore := store.RawScore(5000, now.Add(-30*24*time.Hour))
	recentLightScore := store.RawScore(5, now.Add(-30*time.Minute))

	if oldHeavyScore <= 0 || recentLightScore <= 0 {
		t.Errorf("expected positive raw scores, got %f and %f", oldHeavyScore, recentLightScore)
	}
	if recentLightScore >= oldHeavyScore {
		t.Logf("recent light score (%f) vs old heavy score (%f)", recentLightScore, oldHeavyScore)
	}
}

func TestFrecencyStore_QueryGlobalDedupe(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	_ = store.Record(context.Background(), "make build", "/repo/a", 0)
	_ = store.Record(context.Background(), "make build", "/repo/a", 0)
	_ = store.Record(context.Background(), "make build", "/repo/b", 0)

	entries, err := store.QueryGlobal(context.Background(), "make", 10)
	if err != nil {
		t.Fatalf("QueryGlobal failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 deduplicated entry, got %d", len(entries))
	}
	if entries[0].Count != 3 {
		t.Errorf("expected combined count 3 across workspaces, got %d", entries[0].Count)
	}
}

func TestFrecencyStore_Permissions(t *testing.T) {
	tmpRoot := t.TempDir()
	dbDir := filepath.Join(tmpRoot, "subdir", "vuja")
	dbPath := filepath.Join(dbDir, "history.db")

	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("failed to make pre-existing dir: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write dummy existing db file: %v", err)
	}

	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	dirInfo, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("stat dbDir failed: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("expected directory permissions 0700, got %04o", perm)
	}

	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat dbPath failed: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Errorf("expected database file permissions 0600, got %04o", perm)
	}
}

func TestFrecencyStore_SQLiteConfigurationAndContext(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	var journalMode string
	if qErr := store.db.QueryRowContext(context.Background(), "PRAGMA journal_mode;").Scan(&journalMode); qErr != nil {
		t.Fatalf("failed to query journal_mode: %v", qErr)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode 'wal', got '%s'", journalMode)
	}

	var busyTimeout int
	if qErr := store.db.QueryRowContext(context.Background(), "PRAGMA busy_timeout;").Scan(&busyTimeout); qErr != nil {
		t.Fatalf("failed to query busy_timeout: %v", qErr)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout 5000, got %d", busyTimeout)
	}

	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.Record(ctxCanceled, "git status", tmpDir, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled from Record with canceled context, got %v", err)
	}
}

func TestFrecencyStore_NilReceiver(t *testing.T) {
	var nilStore *FrecencyStore
	if err := nilStore.Record(context.Background(), "cmd", "cwd", 0); err != nil {
		t.Errorf("expected nil error on nil store Record, got %v", err)
	}
	if entries, err := nilStore.QueryLocal(context.Background(), "cwd", "", 10); err != nil || entries != nil {
		t.Errorf("expected nil entries and nil error on nil store QueryLocal, got %v, %v", entries, err)
	}
	if entries, err := nilStore.QueryGlobal(context.Background(), "", 10); err != nil || entries != nil {
		t.Errorf("expected nil entries and nil error on nil store QueryGlobal, got %v, %v", entries, err)
	}
	if err := nilStore.Close(); err != nil {
		t.Errorf("expected nil error on nil store Close, got %v", err)
	}
}

func TestFrecencyStore_ExitCodeBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	cwd := "/home/user/test"
	_ = store.Record(context.Background(), "grep foo", cwd, 0) // count=1
	_ = store.Record(context.Background(), "grep foo", cwd, 1) // count unchanged (1)

	entries, _ := store.QueryLocal(context.Background(), cwd, "grep", 10)
	if len(entries) != 1 || entries[0].Count != 1 {
		t.Errorf("expected grep count to be 1 after non-zero exit code, got %v", entries)
	}

	_ = store.RecordTransition(context.Background(), "git checkout", "git status", cwd, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "git status", cwd, 1)

	transitions, isLocal := store.QueryTransitionsWithFallback(context.Background(), "git checkout", cwd)
	if !isLocal || len(transitions) != 1 || transitions[0].Count != 1 {
		t.Errorf("expected transition count 1 after non-zero exit code, got %v, isLocal=%v", transitions, isLocal)
	}
}

func TestFrecencyStore_ExactSequencesAndImportedHistory(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	cwd := "/repo"
	if recordErr := store.RecordExactTransition(ctx, "make build", "make test", cwd, 0); recordErr != nil {
		t.Fatal(recordErr)
	}
	if recordErr := store.RecordExactTransition(ctx, "make build", "make test", cwd, 1); recordErr != nil {
		t.Fatal(recordErr)
	}
	exact, local := store.QueryExactTransitionsWithFallback(ctx, "make build", cwd)
	if !local || len(exact) != 1 || exact[0].NextCommand != "make test" || exact[0].Count != 1 {
		t.Fatalf("unexpected exact transitions: %v, local=%v", exact, local)
	}

	imported := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: time.Now()},
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	global, err := store.QueryGlobal(ctx, "git", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].Cmd != "git status" || global[0].Count != 7 {
		t.Fatalf("expected idempotent imported count 7, got %v", global)
	}
}

func TestFrecencyStore_SkipsUnchangedImportedHistoryWrites(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	imported := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: time.Now()},
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_redundant_history_delete
BEFORE DELETE ON imported_history_entries
BEGIN
	SELECT RAISE(ABORT, 'unexpected imported history rewrite');
END;
`); err != nil {
		t.Fatal(err)
	}

	imported[0].LastUsed = imported[0].LastUsed.Add(time.Minute)
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatalf("unchanged command counts should not rewrite imported history: %v", replaceErr)
	}
}

func TestImportedHistoryPreservesAtuinDirectoryAndMetadata(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	entry := ImportedHistoryEntry{
		Command:     "go test ./...",
		Cwd:         "/repo/service",
		Count:       3,
		LastUsed:    time.Unix(1720000000, 0),
		ExitCode:    0,
		HasExitCode: true,
		Duration:    250 * time.Millisecond,
		Source:      "atuin",
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, []ImportedHistoryEntry{entry}); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	local, err := store.QueryLocal(ctx, entry.Cwd, "go test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Cmd != entry.Command || local[0].Count != 3 {
		t.Fatalf("expected directory-scoped Atuin history, got %+v", local)
	}

	var exitCode, duration int64
	var source string
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT exit_code, duration_ns, source FROM imported_history_entries WHERE cmd = ?`,
		entry.Command,
	).Scan(&exitCode, &duration, &source); err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 || duration != entry.Duration.Nanoseconds() || source != "atuin" {
		t.Fatalf("unexpected imported metadata: exit=%d duration=%d source=%q", exitCode, duration, source)
	}
}

func TestImportedHistoryFingerprintTracksRelativeRecency(t *testing.T) {
	now := time.Now()
	original := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: now},
		{Command: "go test", Count: 3, LastUsed: now.Add(-time.Second)},
	}
	shifted := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: now.Add(time.Minute)},
		{Command: "go test", Count: 3, LastUsed: now.Add(time.Minute - time.Second)},
	}
	reordered := []ImportedHistoryEntry{
		{Command: "go test", Count: 3, LastUsed: now.Add(time.Minute)},
		{Command: "git status", Count: 7, LastUsed: now.Add(time.Minute - time.Second)},
	}

	if importedHistoryFingerprint(original) != importedHistoryFingerprint(shifted) {
		t.Fatal("absolute timestamp changes should not force an import")
	}
	if importedHistoryFingerprint(original) == importedHistoryFingerprint(reordered) {
		t.Fatal("relative recency changes must force an import")
	}
}

func TestFrecencyStore_TransitionCwdIsolationAndDepthFallback(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	projectA := "/repo/a"
	projectB := "/repo/b"

	_ = store.RecordTransition(context.Background(), "git checkout", "npm run dev", projectA, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "go test", projectB, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "go test", projectB, 0)

	// query in project B should return go test (Local) and not npm run dev
	transB, isLocalB := store.QueryTransitionsWithFallback(context.Background(), "git checkout", projectB)
	if !isLocalB || len(transB) != 1 || transB[0].NextSkeleton != "go test" {
		t.Errorf("expected local transition 'go test' for project B, got %v (isLocal=%v)", transB, isLocalB)
	}

	// query in project C (no local data) should fallback to Global (returning both aggregated)
	projectC := "/repo/c"
	transC, isLocalC := store.QueryTransitionsWithFallback(context.Background(), "git checkout", projectC)
	if isLocalC || len(transC) != 2 {
		t.Errorf("expected global transitions for project C, got %v (isLocal=%v)", transC, isLocalC)
	}
	if transC[0].NextSkeleton != "go test" {
		t.Errorf("expected global top transition to be 'go test' (count 2), got %s", transC[0].NextSkeleton)
	}

	// depth fallback test: query deep skeleton with no exact match should fallback to shallower prefix
	_ = store.RecordTransition(context.Background(), "git remote", "git fetch", projectA, 0)
	transDeep, isLocalDeep := store.QueryTransitionsWithFallback(context.Background(), "git remote add", projectA)
	if !isLocalDeep || len(transDeep) != 1 || transDeep[0].NextSkeleton != "git fetch" {
		t.Errorf("expected depth fallback to 'git fetch' from 'git remote', got %v", transDeep)
	}
}

func TestFrecencyStore_PreservesNativeAndImportedHistoryExecutions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	native := HistoryEvent{
		EventKey:    "vuja:native",
		Command:     "just deploy staging",
		Cwd:         "/repo",
		StartedAt:   time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC),
		Duration:    12 * time.Second,
		ExitCode:    0,
		HasExitCode: true,
		Source:      "vuja",
		SessionID:   "session-a",
	}
	if recordErr := store.RecordHistoryEvent(ctx, native); recordErr != nil {
		t.Fatalf("RecordHistoryEvent failed: %v", recordErr)
	}

	imported := []HistoryEvent{
		{EventKey: "atuin:first", Command: "just deploy staging", Cwd: "/repo", StartedAt: native.StartedAt.Add(-time.Hour), Source: "atuin", Imported: true},
		{EventKey: "atuin:second", Command: "just deploy staging", Cwd: "/repo", StartedAt: native.StartedAt.Add(-2 * time.Hour), Source: "atuin", Imported: true},
	}
	if replaceErr := store.ReplaceImportedHistoryEvents(ctx, imported); replaceErr != nil {
		t.Fatalf("ReplaceImportedHistoryEvents failed: %v", replaceErr)
	}
	if replaceErr := store.ReplaceImportedHistoryEvents(ctx, imported[:1]); replaceErr != nil {
		t.Fatalf("second ReplaceImportedHistoryEvents failed: %v", replaceErr)
	}

	events, err := store.QueryHistoryEvents(ctx, 100)
	if err != nil {
		t.Fatalf("QueryHistoryEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected one native and one imported execution, got %+v", events)
	}
	if events[0].EventKey != native.EventKey || !events[0].StartedAt.Equal(native.StartedAt) ||
		events[0].Duration != native.Duration || events[0].SessionID != native.SessionID {
		t.Fatalf("expected native execution metadata to survive import replacement, got %+v", events[0])
	}
	if events[1].EventKey != "atuin:first" {
		t.Fatalf("expected stale imported execution to be replaced, got %+v", events)
	}
}

func TestFrecencyStore_AddsHistoryEventsToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := legacy.ExecContext(t.Context(), `CREATE TABLE history_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		cmd TEXT NOT NULL,
		cwd TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_used TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(cmd, cwd)
	)`); createErr != nil {
		t.Fatal(createErr)
	}
	if closeErr := legacy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatalf("expected backward-compatible schema initialization, got %v", err)
	}
	defer store.Close()
	event := HistoryEvent{EventKey: "vuja:migrated", Command: "git status", StartedAt: time.Now(), Source: "vuja"}
	if err := store.RecordHistoryEvent(context.Background(), event); err != nil {
		t.Fatalf("expected new history events table to be usable, got %v", err)
	}
}

func TestFrecencyStore_SkipsUnchangedImportedHistoryEventWrites(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	events := []HistoryEvent{{
		EventKey:  "atuin:first",
		Command:   "just deploy staging",
		StartedAt: time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC),
		Source:    "atuin",
	}}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
CREATE TRIGGER reject_history_event_rewrite
BEFORE DELETE ON history_events
WHEN OLD.imported = 1
BEGIN
    SELECT RAISE(ABORT, 'unchanged history events were rewritten');
END;
`); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatalf("expected unchanged import to skip writes, got %v", err)
	}
}

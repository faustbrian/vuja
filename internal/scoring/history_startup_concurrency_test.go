package scoring

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFrecencyStoreConcurrentMigratedStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	seed, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.db.ExecContext(t.Context(), `WITH RECURSIVE n(x) AS (
		VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2500
	) INSERT INTO history_events
	(event_key, command, normalized_command, started_at, submitted_at, source, state)
	SELECT 'fixture:'||x, 'fixture', 'fixture', '2026-10-21', '2026-10-21', 'vuja', 'submitted' FROM n`)
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	for round := range 4 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() {
				<-start
				store, err := NewFrecencyStore(path)
				if err != nil {
					errs <- err
					return
				}
				errs <- store.Close()
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("simultaneous restored-pane startup round %d: %v", round, err)
			}
		}
	}
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows, changed int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*), COUNT(*) FILTER (
		WHERE command <> 'fixture' OR normalized_command <> 'fixture'
		OR source <> 'vuja' OR state <> 'submitted') FROM history_events`).Scan(&rows, &changed); err != nil {
		t.Fatal(err)
	}
	if rows != 2500 || changed != 0 {
		t.Fatalf("concurrent startup changed retained history: rows=%d changed=%d", rows, changed)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private database permissions not preserved: %v %v", info, err)
	}
	var synchronous int
	if err := store.db.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("startup must retain FULL durability: synchronous=%d err=%v", synchronous, err)
	}
}

func TestFrecencyStoreConcurrentStartupProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	seed, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	type worker struct {
		cmd    *exec.Cmd
		input  io.WriteCloser
		output *bytes.Buffer
	}
	var workers []worker
	for index := range 8 {
		// This is the current package's test executable, never the Vuja app.
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFrecencyStoreStartupProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "HISTORY_STARTUP_FIXTURE_PATH="+path, fmt.Sprintf("HISTORY_STARTUP_FIXTURE_ID=%d", index))
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Errorf("fixture stdin: %v", err)
			continue
		}
		output := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			_ = input.Close()
			t.Errorf("start fixture process: %v", err)
			continue
		}
		workers = append(workers, worker{cmd, input, output})
	}
	// Release only after every child has started. Pair every successful Start
	// with Wait, including failures, before removing the private fixture.
	for _, worker := range workers {
		_, _ = worker.input.Write([]byte{1})
		_ = worker.input.Close()
	}
	for _, worker := range workers {
		if err := worker.cmd.Wait(); err != nil {
			t.Errorf("fixture startup: %v: %s", err, worker.output.String())
		}
	}
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("acknowledged process submissions must survive reopen exactly once: %d", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		if seen[event.EventKey] || event.Command != "printf startup-fixture" || event.State != "running" {
			t.Fatalf("unexpected persisted process submission: %+v", event)
		}
		seen[event.EventKey] = true
	}
}

func TestFrecencyStoreStartupProcess(t *testing.T) {
	path := os.Getenv("HISTORY_STARTUP_FIXTURE_PATH")
	if path == "" {
		t.Skip("owned subprocess fixture only")
	}
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "process:" + os.Getenv("HISTORY_STARTUP_FIXTURE_ID"),
		Command:  "printf startup-fixture", Cwd: "/fixture", Source: "vuja", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFrecencyStoreConcurrentStartupRetention(t *testing.T) {
	for _, policy := range []string{"count", "age"} {
		t.Run(policy, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			var stores []*FrecencyStore
			for range 8 {
				store, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				stores = append(stores, store)
			}
			if _, err := stores[0].db.ExecContext(t.Context(), `WITH RECURSIVE n(x) AS (
				VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<256
			) INSERT INTO history_events
			(event_key, command, normalized_command, submitted_at, started_at, completed_at, exit_code, source, state)
			SELECT 'retained:'||x, 'printf fixture', 'printf fixture',
			CASE WHEN x=256 THEN '2026-10-22T00:00:00Z' ELSE '2026-10-01T00:00:00Z' END,
			CASE WHEN x=256 THEN '2026-10-22T00:00:00Z' ELSE '2026-10-01T00:00:00Z' END,
			CASE WHEN x=256 THEN '2026-10-22T00:00:00Z' ELSE '2026-10-01T00:00:00Z' END,
			0, 'vuja', 'completed' FROM n`); err != nil {
				t.Fatal(err)
			}
			limit, cutoff := 1, time.Time{}
			if policy == "age" {
				limit, cutoff = 0, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
			}
			start := make(chan struct{})
			removed := make(chan int64, len(stores))
			var wg sync.WaitGroup
			for _, store := range stores {
				wg.Go(func() {
					<-start
					count, err := store.PruneHistoryEventsWithRetention(t.Context(), limit, cutoff)
					if err != nil {
						t.Errorf("restored-pane retention: %v", err)
					}
					removed <- count
				})
			}
			close(start)
			wg.Wait()
			close(removed)
			var total int64
			for count := range removed {
				total += count
			}
			events, err := stores[0].QueryAllHistoryEvents(t.Context())
			if err != nil || len(events) != 1 || events[0].EventKey != "retained:256" || total != 255 {
				t.Fatalf("retention must remove each eligible execution once and preserve newest: removed=%d events=%+v err=%v", total, events, err)
			}
		})
	}
}

func TestFrecencyStoreInitializationHonorsHeldWriterBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	holder, err := other.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.ExecContext(t.Context(), `UPDATE metadata SET value = value WHERE key = 'history_schema_version'`); err != nil {
		t.Fatal(err)
	}
	// Constrain the fixture's connection policy, rather than waiting five
	// seconds per regression. The constructor's normal policy is unchanged.
	store.db.SetMaxOpenConns(1)
	if _, err := store.db.ExecContext(t.Context(), `PRAGMA busy_timeout=50`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	if err := store.ensureHistoryEventLifecycleColumns(ctx); err == nil {
		t.Fatal("initialization must fail while another writer retains ownership")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("initialization exceeded its bounded lock budget: %s", elapsed)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureHistoryEventLifecycleColumns(t.Context()); err != nil {
		t.Fatalf("released writer must permit a clean retry: %v", err)
	}
}

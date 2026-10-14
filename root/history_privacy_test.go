package root

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/cobra"
)

func TestHistoryPrivacyImportedForgetSurvivesRealAdapterSourceFallback(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		gap     time.Duration
		partial bool
		native  bool
		late    bool
	}{
		{"full-hour", time.Hour, false, false, false},
		{"full-second", time.Second, false, false, false},
		{"partial-second", time.Second, true, false, false},
		{"partial-second-native", time.Second, true, true, false},
		{"partial-second-late-native", time.Second, true, false, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			gap := fixture.gap
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			originalRecall := integration.RichHistorySnapshot()
			t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			atuinPath := integration.DefaultAtuinHistoryPath(home)
			if err := os.MkdirAll(filepath.Dir(atuinPath), 0o700); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", atuinPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `CREATE TABLE history (
        id TEXT NOT NULL, command TEXT NOT NULL, timestamp INTEGER NOT NULL,
        duration INTEGER NOT NULL, exit INTEGER NOT NULL, cwd TEXT NOT NULL,
        hostname TEXT NOT NULL, session TEXT NOT NULL, deleted_at INTEGER
    )`); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			for index, id := range []string{"forgotten", "later"} {
				if fixture.partial && index == 1 {
					continue // Only the shell observed the independent repetition.
				}
				if _, err := db.ExecContext(t.Context(), `INSERT INTO history (id, command, timestamp, duration, exit, cwd, hostname, session)
            VALUES (?, 'ssh forge@one', ?, 1000000000, 0, '/repo', 'fixture', 'fixture')`, id, at.Add(time.Duration(index)*gap).UnixNano()); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			shellPath := filepath.Join(home, ".zsh_history")
			contents := fmt.Sprintf(": %d:1;ssh forge@one\n: %d:1;ssh forge@one\n", at.Unix(), at.Add(gap).Unix())
			if err := os.WriteFile(shellPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Get()
			previousPath := cfg.History.Integrations.Shell.Path
			cfg.History.Integrations.Shell.Path = shellPath
			t.Cleanup(func() { cfg.History.Integrations.Shell.Path = previousPath })
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := scoring.NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			recordNative := func() {
				t.Helper()
				event := scoring.HistoryEvent{EventKey: "native-first", Command: "ssh forge@one", Cwd: "/repo", Source: "vuja", StartedAt: at, SessionID: "fixture"}
				if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				event.HasExitCode, event.CompletedAt, event.Duration = true, at.Add(time.Second), time.Second
				if _, err := store.CompleteHistoryEvent(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.native {
				recordNative()
			}
			if err := importSelectedHistory(store, "zsh", true, true); err != nil {
				t.Fatal(err)
			}
			initial, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(initial) != 2 {
				t.Fatalf("initial adapters must represent exactly two executions: %+v %v", initial, err)
			}
			if fixture.late {
				recordNative() // Arrival after the adapter already hid its shell copy.
			}
			if fixture.native {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := scoring.NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				for range 2 {
					if err := importSelectedHistory(reopened, "zsh", true, true); err != nil {
						t.Fatal(err)
					}
					events, err := reopened.QueryAllHistoryEvents(t.Context())
					if err != nil || len(events) != 2 {
						t.Fatalf("native preference lost an independent shell repetition: %+v %v", events, err)
					}
					ids := make(map[string]bool)
					for _, event := range events {
						ids[event.EventKey] = true
						if event.EventKey != "native-first" && (event.Source != "zsh" || !event.StartedAt.Equal(at.Add(gap))) {
							t.Fatalf("wrong surviving shell execution: %+v", event)
						}
					}
					if !ids["native-first"] || len(integration.RichHistorySnapshot()) != 2 {
						t.Fatal("native/source publication changed execution identity or cardinality")
					}
					global, err := reopened.QueryGlobal(t.Context(), "ssh", 10)
					if err != nil || len(global) != 1 || global[0].Count != 2 {
						t.Fatalf("native/source import changed global usage: %+v %v", global, err)
					}
				}
				return
			}
			var id string
			if fixture.late {
				id = "native-first"
			} else {
				for _, event := range initial {
					if event.Source == "atuin" && event.StartedAt.Equal(at) {
						id = event.EventKey
					}
				}
			}
			if id == "" {
				t.Fatal("Atuin did not provide the known execution identity")
			}
			selected, pending, err := queryHistoryForgetSelection(t.Context(), store, id, "", "")
			if err != nil || len(selected) != 1 {
				t.Fatalf("preview: %+v %v", selected, err)
			}
			cmd := &cobra.Command{}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := forgetSelectedHistory(t.Context(), store, selected, pending, cmd); err != nil {
				t.Fatal(err)
			}
			expectedSource, expectedCWD := "atuin", "/repo"
			if fixture.partial {
				expectedSource, expectedCWD = "zsh", ""
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].Source != expectedSource || !events[0].StartedAt.Equal(at.Add(gap)) {
				t.Fatalf("forget removed an unselected execution: %+v %v", events, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := scoring.NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for _, refreshAtuin := range []bool{false, true, false} {
				if err := importSelectedHistory(reopened, "zsh", refreshAtuin, true); err != nil {
					t.Fatal(err)
				}
				events, err := reopened.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != 1 || events[0].Source != expectedSource || !events[0].StartedAt.Equal(at.Add(gap)) {
					t.Fatalf("source fallback resurrected forgotten or duplicated surviving executions: %+v %v", events, err)
				}
				recall := integration.RichHistorySnapshot()
				if len(recall) != 1 || recall[0].Source != expectedSource || !recall[0].StartedAt.Equal(at.Add(gap)) {
					t.Fatalf("source fallback published incorrect recall: %+v", recall)
				}
				entries, err := reopened.QueryLocal(t.Context(), expectedCWD, "ssh", 10)
				if err != nil || len(entries) != 1 || entries[0].Count != 1 || !entries[0].LastUsed.Equal(at.Add(gap)) {
					t.Fatalf("source fallback restored or inflated imported ranking: %+v %v", entries, err)
				}
				global, err := reopened.QueryGlobal(t.Context(), "ssh", 10)
				if err != nil || len(global) != 1 || global[0].Count != 1 {
					t.Fatalf("source fallback duplicated global usage evidence: %+v %v", global, err)
				}
			}
		})
	}
}

func TestHistoryPrivacyNativeForgetUsesStrongestEvidenceInPartialImportSnapshot(t *testing.T) {
	for _, test := range []struct {
		name         string
		retainedCopy bool
	}{{"only forgotten copy", false}, {"only retained copy", true}} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			originalRecall := integration.RichHistorySnapshot()
			t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := scoring.NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			copyAt := at
			if test.retainedCopy {
				copyAt = at.Add(-300 * time.Millisecond)
			}
			copy := scoring.HistoryEvent{EventKey: "atuin:partial", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: copyAt, State: "completed", HasExitCode: true, Occurrences: 1}
			if err := store.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{copy}); err != nil {
				t.Fatal(err)
			}
			for index, id := range []string{"native:retained", "native:forgotten"} {
				event := copy
				event.EventKey, event.Source, event.Imported = id, "vuja", false
				event.StartedAt = at.Add(time.Duration(index-1) * 300 * time.Millisecond)
				event.CompletedAt = event.StartedAt.Add(time.Second)
				if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
					t.Fatalf("delayed native completion: completed=%v error=%v", completed, err)
				}
			}
			selected, pending, err := queryHistoryForgetSelection(t.Context(), store, "native:forgotten", "", "")
			if err != nil || len(selected) != 1 {
				t.Fatalf("preview: %+v %v", selected, err)
			}
			cmd := &cobra.Command{}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := forgetSelectedHistory(t.Context(), store, selected, pending, cmd); err != nil {
				t.Fatal(err)
			}
			assertStored := func(current *scoring.FrecencyStore, keepCopy bool) {
				t.Helper()
				events, err := current.QueryAllHistoryEvents(t.Context())
				want := 1
				if keepCopy {
					want++
				}
				if err != nil || len(events) != want {
					t.Fatalf("partial snapshot selected the wrong counterpart: %+v %v", events, err)
				}
				for _, event := range events {
					if event.EventKey != "native:retained" && !(keepCopy && event.EventKey == copy.EventKey) {
						t.Fatalf("unexpected survivor: %+v", event)
					}
				}
			}
			assertStored(store, test.retainedCopy)
			for _, entry := range integration.RichHistorySnapshot() {
				if entry.ID == "native:forgotten" || !test.retainedCopy && entry.ID == copy.EventKey {
					t.Fatalf("recall retained the forgotten representation: %+v", entry)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := scoring.NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertStored(reopened, test.retainedCopy)
			if err := reopened.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{copy}); err != nil {
				t.Fatal(err)
			}
			// Native deduplication may now remove the retained copy; in neither
			// variant may external refresh restore the forgotten execution.
			assertStored(reopened, false)
		})
	}
}

func TestHistoryPrivacyNativeForgetImmediatelyReconcilesEarlierImportedCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	originalRecall := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	copy := scoring.HistoryEvent{EventKey: "atuin:earlier-copy", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, CompletedAt: at.Add(time.Second), Duration: time.Second, HasExitCode: true, State: "completed", Occurrences: 1}
	later := copy
	later.EventKey, later.StartedAt, later.CompletedAt = "atuin:later-repeat", at.Add(time.Hour), at.Add(time.Hour+time.Second)
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{copy, later}); err != nil {
		t.Fatal(err)
	}
	native := copy
	native.EventKey, native.Source, native.Imported = "native:delayed", "vuja", false
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	if completed, err := store.CompleteHistoryEvent(t.Context(), native); err != nil || !completed {
		t.Fatalf("delayed native completion: completed=%v error=%v", completed, err)
	}
	selected, pending, err := queryHistoryForgetSelection(t.Context(), store, native.EventKey, "", "")
	if err != nil || len(selected) != 1 {
		t.Fatalf("preview native identity: %+v %v", selected, err)
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := forgetSelectedHistory(t.Context(), store, selected, pending, cmd); err != nil {
		t.Fatal(err)
	}
	assertSurvivor := func(current *scoring.FrecencyStore) {
		t.Helper()
		events, err := current.QueryAllHistoryEvents(t.Context())
		if err != nil || len(events) != 1 || events[0].EventKey != later.EventKey {
			t.Fatalf("successful forgetting left an earlier external representation: %+v %v", events, err)
		}
		entries, err := current.QueryLocal(t.Context(), "/repo", "ssh", 10)
		if err != nil || len(entries) != 1 || entries[0].Count != 1 || !entries[0].LastUsed.Equal(later.StartedAt) {
			t.Fatalf("forgotten imported evidence remains in ranking: %+v %v", entries, err)
		}
	}
	assertSurvivor(store)
	recall := integration.RichHistorySnapshot()
	if len(recall) != 1 || recall[0].ID != later.EventKey {
		t.Fatalf("recall publication retained the earlier copy: %+v", recall)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertSurvivor(reopened)
	if err := reopened.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{copy, later}); err != nil {
		t.Fatal(err)
	}
	assertSurvivor(reopened)
}

func TestHistoryPrivacyImportedForgetDiscardsDelayedNativeJournalCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	originalRecall := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	imported := scoring.HistoryEvent{EventKey: "atuin:journal-copy", Command: "ssh forge@journal-copy", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []scoring.HistoryEvent{imported}); err != nil {
		t.Fatal(err)
	}
	native := historyEventEntry(imported)
	native.ID, native.Source, native.HasExitCode = "native:journal-copy", "vuja", false
	native.State = integration.HistoryStateRunning
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, native); err != nil {
		t.Fatal(err)
	}
	native.HasExitCode, native.CompletedAt = true, at.Add(time.Second)
	native.State = integration.HistoryStateCompleted
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, native); err != nil {
		t.Fatal(err)
	}
	selected, pending, err := queryHistoryForgetSelection(t.Context(), store, imported.EventKey, "", "")
	if err != nil || len(selected) != 1 || len(pending) != 0 {
		t.Fatalf("imported-ID preview: selected=%+v pending=%+v error=%v", selected, pending, err)
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := forgetSelectedHistory(t.Context(), store, selected, pending, cmd); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("recovery restored the forgotten execution under its native ID: %+v %v", events, err)
	}
	if recall := integration.RichHistorySnapshot(); len(recall) != 0 {
		t.Fatalf("forgotten native journal copy was published: %+v", recall)
	}
	if journal, err := previewHistoryRecoveryJournal(t.Context()); err != nil || len(journal) != 0 {
		t.Fatalf("successful forgetting left the native journal copy pending: %+v %v", journal, err)
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, native); !errors.Is(err, scoring.ErrHistoryForgotten) {
		t.Fatalf("late raw journal append bypassed the native alias exclusion: %v", err)
	}
}

func TestHistoryPrivacyForgetConfirmationRejectsConcurrentCanonicalReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	previewed := scoring.HistoryEvent{EventKey: "atuin:replaceable", Command: "ssh forge@previewed", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", Occurrences: 1}
	if err := other.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{previewed}); err != nil {
		t.Fatal(err)
	}
	selection, _, err := queryHistoryForgetSelection(t.Context(), store, previewed.EventKey, "", "")
	if err != nil || len(selection) != 1 {
		t.Fatalf("selection: %+v %v", selection, err)
	}
	// Pause the actual deletion adapter after preview/token validation, before
	// it can acquire writer ownership. A second real store replaces this ID.
	canonicalHistoryMutationMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			canonicalHistoryMutationMu.Unlock()
		}
	})
	entered, result := make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	go func() {
		close(entered)
		result <- forgetSelectedHistory(ctx, store, selection, nil, cmd)
	}()
	<-entered
	replacement := previewed
	replacement.Command, replacement.Cwd, replacement.StartedAt = "git status", "/other", at.Add(time.Hour)
	if err := other.ReplaceExternalHistoryEvents(t.Context(), []scoring.HistoryEvent{replacement}); err != nil {
		t.Fatal(err)
	}
	canonicalHistoryMutationMu.Unlock()
	locked = false
	if err := <-result; err == nil {
		t.Fatalf("confirmation deleted an unpreviewed replacement: %q", output.String())
	}
	if events, err := other.QueryHistoryEventsByKeys(t.Context(), []string{replacement.EventKey}); err != nil || len(events) != 1 || events[0].Command != replacement.Command {
		t.Fatalf("replacement must survive rejected confirmation: %+v %v", events, err)
	}
	if forgotten, err := other.HistoryEventForgotten(t.Context(), replacement.EventKey); err != nil || forgotten {
		t.Fatalf("rejected confirmation installed an exclusion: %v %v", forgotten, err)
	}
}

func TestHistoryPrivacyForgetRequestsAreNotRecorded(t *testing.T) {
	for _, command := range []string{"vuja history forget --id first", "vuja history forget --command 'ssh forge@one'", "/tmp/bin/vuja history forget --command=private-command", "vuja --debug history forget --command 'ssh forge@one'", "vuja -s zsh history forget --command 'ssh forge@one'", "vuja --shell=zsh -d history forget --id first", "true; vuja history forget --command 'private-command'", "true && vuja history forget --id first", "printf before\nvuja history forget --id first", "printf 'vuja history forget'"} {
		if _, allowed := historyRecordableCommand(command, false); allowed {
			t.Fatalf("wrapper would retain a forgetting request: %q", command)
		}
		if _, allowed := policy.HistoryCommand(command, false); allowed {
			t.Fatalf("imports/derived feedback would retain a forgetting request: %q", command)
		}
	}
	for _, command := range []string{"vuja history stats", "git status"} {
		if _, allowed := historyRecordableCommand(command, false); !allowed {
			t.Fatalf("privacy action gate rejected an unrelated command: %q", command)
		}
	}
}

func TestHistoryPrivacyCompoundForgetRequestCannotBeJournaledOrMirrored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	for _, command := range []string{
		"true && vuja history forget --command 'private-command'",
		"command vuja history forget --command 'private-command'",
		"VUJA_MODE=local command vuja history forget --command 'private-command'",
		"env vuja history forget --command 'private-command'",
		"command /usr/bin/env -u OTHER VUJA_MODE=local vuja history forget --command 'private-command'",
		"! vuja history forget --command 'private-command'",
		"time vuja history forget --command 'private-command'",
		"! VUJA_MODE=local vuja history forget --command 'private-command'",
		"time VUJA_MODE=local vuja history forget --command 'private-command'",
		"printf 'vuja history forget --command private-command'",
	} {
		entry := integration.HistoryEntry{ID: "compound-private", Command: command, Cwd: "/repo", Source: "vuja", StartedAt: time.Now().UTC()}
		if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err == nil {
			t.Fatalf("privacy request was accepted by recovery journal: %q", command)
		}
		path, err := historyRecoveryJournalPath()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("private request created a recovery journal: %v", err)
		}
		for _, shell := range []string{"bash", "zsh", "fish"} {
			if record, allowed := recordableNativeHistoryRecord(entry.Command, shell, entry.StartedAt); allowed || record != "" {
				t.Fatalf("private request entered %s mirror: %q", shell, record)
			}
		}
	}
}

func TestHistoryPrivacyNativeForgetExcludesRealShellCounterpartAfterRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"native-forget", "native-later"} {
		event := scoring.HistoryEvent{EventKey: id, Command: "ssh forge@one", Cwd: "/repo", Source: "vuja", SessionID: "fixture", StartedAt: at.Add(time.Duration(index) * time.Hour)}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		event.HasExitCode, event.CompletedAt = true, event.StartedAt.Add(time.Second)
		if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
			t.Fatalf("fixture completion: completed=%v error=%v", completed, err)
		}
	}
	path := filepath.Join(home, ".zsh_history")
	contents := fmt.Sprintf(": %d:0;ssh forge@one\n: %d:0;ssh forge@one\n", at.Unix(), at.Add(time.Hour).Unix())
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Get()
	previousPath := cfg.History.Integrations.Shell.Path
	cfg.History.Integrations.Shell.Path = path
	t.Cleanup(func() { cfg.History.Integrations.Shell.Path = previousPath })
	if err := importSelectedHistory(store, "zsh", false, true); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatalf("native executions must initially deduplicate their external copies: %+v %v", events, err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{"native-forget"}); err != nil {
		t.Fatal(err)
	}
	if err := importSelectedHistory(store, "zsh", false, true); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != "native-later" {
		t.Fatalf("real source refresh resurrected the forgotten native execution: %+v %v", events, err)
	}
	entries, err := store.QueryLocal(t.Context(), "/repo", "ssh", 10)
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("external refresh restored forgotten ranking evidence: %+v %v", entries, err)
	}
}

func TestHistoryPrivacyForgetSelectsJournalOnlyExecutionWithoutReplayingPreview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := integration.HistoryEntry{ID: "journal:forget", Command: "ssh forge@pending", NormalizedCommand: "ssh forge@pending", Cwd: "/repo", Source: "vuja", StartedAt: at, SubmittedAt: at, State: integration.HistoryStateRunning}
	kept := forgotten
	kept.ID, kept.Command, kept.NormalizedCommand = "journal:keep", "git status", "git status"
	for _, entry := range []integration.HistoryEntry{forgotten, kept} {
		if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
			t.Fatal(err)
		}
	}
	forgotten.HasExitCode, forgotten.CompletedAt, forgotten.State = true, at.Add(time.Second), integration.HistoryStateCompleted
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, forgotten); err != nil {
		t.Fatal(err)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newHistoryForgetCommand()
	if err := cmd.Flags().Set("id", forgotten.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`confirmation ([0-9a-f]{64})`).FindStringSubmatch(output.String())
	if len(token) != 2 || !strings.Contains(output.String(), "matched 1 execution records") {
		t.Fatalf("journal-only execution is missing from preview: %q", output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preview changed pending durable records: error=%v", err)
	}
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("preview replayed or inserted canonical history: %+v %v", events, err)
	}
	if err := cmd.Flags().Set("confirm", token[1]); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, forgotten); !errors.Is(err, scoring.ErrHistoryForgotten) {
		t.Fatalf("late completion must not restore forgotten recovery bytes: %v", err)
	}
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != kept.ID {
		t.Fatalf("confirmed journal forgetting lost its survivor or allowed replay resurrection: %+v %v", events, err)
	}
	if !strings.Contains(output.String(), "forgotten 1 execution records") {
		t.Fatalf("journal target was not included in the confirmed result: %q", output.String())
	}
}

func TestHistoryPrivacyJournalPreviewHonorsAcknowledgementAndCanonicalIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{ID: "acknowledged", Command: "ssh forge@old", Source: "vuja", Cwd: "/repo", StartedAt: at}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHistoryRecoveryOffset(info.Size()); err != nil {
		t.Fatal(err)
	}
	entry.ID, entry.Command = "canonical-wins", "ssh forge@stale"
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	canonical := scoringHistoryEvent(entry)
	canonical.Command, canonical.NormalizedCommand = "git status", "git status"
	if err := store.RecordHistorySubmission(t.Context(), canonical); err != nil {
		t.Fatal(err)
	}
	events, pending, err := queryHistoryForgetSelection(t.Context(), store, "", "ssh forge@stale", "")
	if err != nil || len(events) != 0 || len(pending) != 0 {
		t.Fatalf("stale journal payload selected canonical command: %+v %+v %v", events, pending, err)
	}
	events, _, err = queryHistoryForgetSelection(t.Context(), store, "acknowledged", "", "")
	if err != nil || len(events) != 0 {
		t.Fatalf("acknowledged bytes were offered as pending history: %+v %v", events, err)
	}
}

func TestHistoryPrivacyJournalPreviewRejectsConflictingAndUnsafeFilesWithoutRepair(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	entry := integration.HistoryEntry{ID: "conflict", Command: "git status", Source: "vuja", Cwd: "/repo", StartedAt: time.Now().UTC()}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	entry.Command = "ssh forge@changed"
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := previewHistoryRecoveryJournal(t.Context()); err == nil {
		t.Fatal("conflicting payloads were silently merged")
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := previewHistoryRecoveryJournal(t.Context()); err == nil {
		t.Fatal("preview accepted a non-private journal")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("preview changed file permissions: %v %v", info, err)
	}
}

func TestHistoryPrivacyNilStorePublicationCannotRestoreForgottenExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path, err := scoring.DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	integration.PublishCanonicalHistory(nil)
	entry := integration.HistoryEntry{ID: "fallback-forgotten", Command: "ssh forge@private", Source: "vuja", Cwd: "/repo", StartedAt: time.Now().UTC()}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{integration.HistoryStateRunning, integration.HistoryStateCompleted} {
		entry.State = state
		publishHistorySubmission(nil, entry)
	}
	for _, observed := range integration.RichHistorySnapshot() {
		if observed.ID == entry.ID {
			t.Fatalf("nil-store publication bypassed durable privacy exclusion: %+v", observed)
		}
	}
	entry.ID = "fallback-survivor"
	publishHistorySubmission(nil, entry)
	found := false
	for _, observed := range integration.RichHistorySnapshot() {
		found = found || observed.ID == entry.ID
	}
	if !found {
		t.Fatal("readable exclusion state must preserve unrelated fallback recall")
	}
}

func TestHistoryPrivacyNilStorePublicationDefersRecallWhenExclusionCannotBeRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	integration.PublishCanonicalHistory(nil)
	entry := integration.HistoryEntry{ID: "unverified-fallback", Command: "git status", Source: "vuja"}
	publishHistorySubmission(nil, entry)
	if got := integration.RichHistorySnapshot(); len(got) != 0 {
		t.Fatalf("missing exclusion authority must not be assumed empty: %+v", got)
	}
	path, err := scoring.DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only publication created a database: %v", err)
	}
}

func TestHistoryPrivacyForgottenLateJournalWritesDoNotRestoreRawTextOrHealthFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path, err := scoring.DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := integration.HistoryEntry{ID: "late-private-record", Command: "ssh forge@private", Source: "vuja", StartedAt: time.Now().UTC(), Cwd: "/repo"}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	keeper := entry
	keeper.ID, keeper.Command = "journal-survivor", "git status"
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, keeper); err != nil {
		t.Fatal(err)
	}
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{historyRecoverySubmission, historyRecoveryCompletion} {
		if err := appendHistoryRecoveryRecord(kind, entry); !errors.Is(err, scoring.ErrHistoryForgotten) {
			t.Fatalf("late %s copied forgotten raw text back into recovery: %v", kind, err)
		}
	}
	entry.CompletedAt, entry.HasExitCode, entry.State = entry.StartedAt.Add(time.Second), true, integration.HistoryStateCompleted
	failures := historyPersistenceFailures.Load()
	observed := false
	observe := func(bool, bool) { observed = true }
	if persisted, journaled := persistHistorySubmissionDurablyObserved(nil, entry, observe); persisted || journaled {
		t.Fatalf("forgotten nil-store submission was accepted: persisted=%v journaled=%v", persisted, journaled)
	}
	if completed, journaled := completeHistoryEventDurablyObserved(nil, entry, observe); completed || journaled {
		t.Fatalf("forgotten nil-store completion was accepted: completed=%v journaled=%v", completed, journaled)
	}
	if observed || historyPersistenceFailures.Load() != failures {
		t.Fatal("deliberate privacy rejection reported a persistence failure")
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("forgotten late write changed durable recovery bytes: %v", err)
	}
}

func TestHistoryPrivacyForgetPreviewDisclosesCompactExecutionsAndLegacyLearning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	event := scoring.HistoryEvent{EventKey: "legacy:compact-preview", Command: "ssh forge@compact", NormalizedCommand: "ssh forge@compact", Cwd: "/repo", Source: "legacy-vuja", StartedAt: at, CompletedAt: at.Add(time.Second), HasExitCode: true, State: "completed", Occurrences: 7}
	if err := store.RecordHistoryEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFeedback(t.Context(), event.Command, event.Cwd, "typed"); err != nil {
		t.Fatal(err)
	}
	cmd := newHistoryForgetCommand()
	if err := cmd.Flags().Set("id", event.EventKey); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "occurrences 7") {
		t.Fatalf("preview hides the seven logical executions represented by this selected record: %q", output.String())
	}
	if !strings.Contains(output.String(), "legacy learning") {
		t.Fatalf("preview does not disclose the unattributed-learning limitation: %q", output.String())
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].Occurrences != 7 {
		t.Fatalf("preview changed compact history: %+v %v", events, err)
	}
}

func TestHistoryPrivacyForgetConfirmationBindsLogicalOccurrenceScope(t *testing.T) {
	event := scoring.HistoryEvent{EventKey: "compact", Command: "git status", Source: "legacy-vuja", Cwd: "/repo", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Occurrences: 7}
	before := historyForgetConfirmation([]scoring.HistoryEvent{event})
	event.Occurrences = 2
	if token := historyForgetConfirmation([]scoring.HistoryEvent{event}); token == before {
		t.Fatal("confirmation did not bind the logical executions represented by the selected record")
	}
	event.Occurrences = 0 // A pending native submission defaults to one.
	pending := historyForgetConfirmation([]scoring.HistoryEvent{event})
	event.Occurrences, event.State, event.HasExitCode = 1, "completed", true
	event.CompletedAt = event.StartedAt.Add(time.Second)
	if token := historyForgetConfirmation([]scoring.HistoryEvent{event}); token != pending {
		t.Fatal("normalizing a pending single execution or receiving completion invalidated its identity-bound confirmation")
	}
}

// Goal QA3: migration groups are multiple logical executions, even when their
// last timestamp also identifies a single execution in another source.
func TestHistoryPrivacyForgetRefusesImplicitCompactCounterpartScope(t *testing.T) {
	for _, importedCompact := range []bool{true, false} {
		t.Run(fmt.Sprintf("imported-compact-%t", importedCompact), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			scoring.CloseGlobalFrecencyStore()
			t.Cleanup(scoring.CloseGlobalFrecencyStore)
			originalRecall := integration.RichHistorySnapshot()
			t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
			path, err := scoring.DefaultHistoryPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			statement := `CREATE TABLE history_entries (
                cmd TEXT NOT NULL, cwd TEXT NOT NULL, count INTEGER,
                last_used TEXT NOT NULL, UNIQUE(cmd, cwd));
                INSERT INTO history_entries VALUES
                ('ssh forge@compact', '/repo', 7, '2026-10-01T12:00:00Z');`
			if importedCompact {
				statement = `CREATE TABLE imported_history_entries (
                    cmd TEXT NOT NULL, cwd TEXT NOT NULL, count INTEGER,
                    last_used TEXT NOT NULL, exit_code INTEGER,
                    duration_ns INTEGER NOT NULL, source TEXT NOT NULL,
                    UNIQUE(cmd, cwd, source));
                    INSERT INTO imported_history_entries VALUES
                    ('ssh forge@compact', '/repo', 7, '2026-10-01T12:00:00Z', 0, 0, 'atuin');`
			}
			_, migrationErr := legacy.ExecContext(t.Context(), statement)
			closeErr := legacy.Close()
			if migrationErr != nil || closeErr != nil {
				t.Fatalf("legacy fixture: %v %v", migrationErr, closeErr)
			}
			store, err := scoring.GetFrecencyStore()
			if err != nil {
				t.Fatal(err)
			}
			migrated, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(migrated) != 1 || migrated[0].Occurrences != 7 || migrated[0].Imported != importedCompact {
				t.Fatalf("actual migration must preserve seven logical executions: %+v %v", migrated, err)
			}
			single := scoring.HistoryEvent{EventKey: "atuin:single", Command: "ssh forge@compact", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: migrated[0].StartedAt, HasExitCode: true, State: "completed"}
			if importedCompact {
				single.EventKey, single.Source, single.Imported = "native:single", "vuja", false
				if err := appendHistoryRecoveryRecord(historyRecoverySubmission, historyEventEntry(single)); err != nil {
					t.Fatal(err)
				}
				single.CompletedAt = single.StartedAt.Add(time.Second)
				if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, historyEventEntry(single)); err != nil {
					t.Fatal(err)
				}
				if err := replayHistoryRecoveryJournal(store); err != nil {
					t.Fatal(err)
				}
			} else if err := store.ReplaceImportedHistoryEvents(t.Context(), []scoring.HistoryEvent{single}); err != nil {
				t.Fatal(err)
			}
			before, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(before) != 2 {
				t.Fatalf("migration and independent source must coexist: %+v %v", before, err)
			}
			// Use a separate SQLite reader to prove rollback covers the opaque
			// registries and cross-session change feed, not only visible recall.
			registryCounts := func() []int {
				t.Helper()
				reader, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				counts := make([]int, 4)
				for index, table := range []string{"forgotten_history_events", "forgotten_native_history", "history_counterpart_sources", "history_changes"} {
					if err := reader.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&counts[index]); err != nil {
						t.Fatal(err)
					}
				}
				return counts
			}
			beforeRegistries := registryCounts()
			assertUnchanged := func() {
				t.Helper()
				after, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed canonical identities or occurrences: %+v %v", after, err)
				}
				for _, event := range before {
					if forgotten, err := store.HistoryEventForgotten(t.Context(), event.EventKey); err != nil || forgotten {
						t.Fatalf("refusal installed exclusion for %q: %v %v", event.EventKey, forgotten, err)
					}
				}
				if entries, err := store.QueryGlobal(t.Context(), single.Command, 10); err != nil || len(entries) != 1 || entries[0].Count != 8 {
					t.Fatalf("refusal changed logical ranking count: %+v %v", entries, err)
				}
				if after := registryCounts(); !reflect.DeepEqual(beforeRegistries, after) {
					t.Fatalf("refusal changed exclusions, source membership or publication: before %v after %v", beforeRegistries, after)
				}
			}
			assertUnchanged()
			cmd := newHistoryForgetCommand()
			if err := cmd.Flags().Set("id", single.EventKey); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "occurrences 1") || strings.Contains(output.String(), "occurrences 7") {
				t.Fatalf("narrow preview must confirm only the single execution: %q", output.String())
			}
			token := regexp.MustCompile(`confirmation ([0-9a-f]{64})`).FindStringSubmatch(output.String())
			if len(token) != 2 {
				t.Fatalf("missing confirmation: %q", output.String())
			}
			if err := cmd.Flags().Set("confirm", token[1]); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "compact") {
				t.Fatalf("narrow deletion must refuse ambiguous compact scope, not silently remove unconfirmed repeats: %v; output %q", err, output.String())
			}
			assertUnchanged()
			scoring.CloseGlobalFrecencyStore()
			store, err = scoring.GetFrecencyStore()
			if err != nil {
				t.Fatal(err)
			}
			assertUnchanged()
			// Explicitly selecting both records authorizes the whole compact
			// group. Refusal must not make migrated history undeletable.
			selection, pending, err := queryHistoryForgetSelection(t.Context(), store, "", single.Command, "/repo")
			if err != nil || len(selection) != 2 {
				t.Fatalf("whole-group preview: %+v %v", selection, err)
			}
			if err := forgetSelectedHistory(t.Context(), store, selection, pending, cmd); err != nil {
				t.Fatalf("explicit whole-group deletion must remain available: %v", err)
			}
			if after, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(after) != 0 {
				t.Fatalf("explicit selection did not remove its whole scope: %+v %v", after, err)
			}
		})
	}
}

func TestHistoryPrivacyForgettingNavigationRemovesLiveDirectoryEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index, path := range []string{"/forgotten-destination", "/kept-destination"} {
		event := scoring.HistoryEvent{EventKey: []string{"forget-visit", "keep-visit"}[index], Command: "cd " + path, Cwd: "/repo", Source: "vuja", SessionID: "fixture", StartedAt: at.Add(time.Duration(index) * time.Minute)}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		event.HasExitCode, event.CompletedAt = true, event.StartedAt.Add(time.Second)
		if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
			t.Fatalf("fixture completion: completed=%v error=%v", completed, err)
		}
		// Match the live completion producer: actual destinations have
		// execution identities, unlike the deliberately unowned legacy API.
		if err := store.RecordHistoryDirectory(t.Context(), event.EventKey, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceDirectorySource(t.Context(), "zoxide", []scoring.DirectoryImport{{Path: "/external-destination", Count: 3, LastUsed: at}}); err != nil {
		t.Fatal(err)
	}
	cmd := newHistoryForgetCommand()
	if err := cmd.Flags().Set("id", "forget-visit"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`confirmation ([0-9a-f]{64})`).FindStringSubmatch(output.String())
	if len(token) != 2 {
		t.Fatalf("missing confirmation: %q", output.String())
	}
	if err := cmd.Flags().Set("confirm", token[1]); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/forgotten-destination", "/kept-destination", "/external-destination"} {
		entries, err := store.QueryDirectories(t.Context(), path, 10, "balanced")
		if err != nil {
			t.Fatal(err)
		}
		if path == "/forgotten-destination" && len(entries) != 0 {
			t.Fatalf("forgotten-only live directory evidence survived: %+v", entries)
		}
		if path != "/forgotten-destination" && (len(entries) != 1 || entries[0].Path != path) {
			t.Fatalf("unrelated/external directory evidence was lost: path=%s entries=%+v", path, entries)
		}
	}
}

func TestHistoryPrivacyExactCommandMatchesOuterWhitespaceInCanonicalAndJournal(t *testing.T) {
	for _, suffix := range []string{"\t", "\r\n", "\u00a0", "\u2003\u3000"} {
		t.Run(fmt.Sprintf("suffix-%q", suffix), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			scoring.CloseGlobalFrecencyStore()
			t.Cleanup(scoring.CloseGlobalFrecencyStore)
			originalRecall := integration.RichHistorySnapshot()
			t.Cleanup(func() { integration.PublishCanonicalHistory(originalRecall) })
			store, err := scoring.GetFrecencyStore()
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			raw := "git status" + suffix
			for index, event := range []scoring.HistoryEvent{
				{EventKey: "canonical", Command: raw, NormalizedCommand: "git status", Cwd: "/repo"},
				{EventKey: "alias", Command: "gs", NormalizedCommand: "git status", Cwd: "/repo"},
				{EventKey: "prefix-neighbor", Command: "git status --short", Cwd: "/repo"},
				{EventKey: "other-directory", Command: raw, Cwd: "/other"},
			} {
				event.Source, event.StartedAt = "vuja", at.Add(time.Duration(index)*time.Minute)
				if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			pending := integration.HistoryEntry{ID: "pending", Command: raw, NormalizedCommand: "git status", Cwd: "/repo", Source: "vuja", StartedAt: at.Add(30 * time.Second)}
			if err := appendHistoryRecoveryRecord(historyRecoverySubmission, pending); err != nil {
				t.Fatal(err)
			}
			cmd := newHistoryForgetCommand()
			for flag, value := range map[string]string{"command": raw, "cwd": "/repo"} {
				if err := cmd.Flags().Set(flag, value); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatal(err)
			}
			preview := output.String()
			if !strings.Contains(preview, "matched 2 execution records") || !strings.Contains(preview, `id "canonical"`) || !strings.Contains(preview, `id "pending"`) {
				t.Fatalf("canonical and journal must match the same outer-whitespace rule: %q", preview)
			}
			for _, id := range []string{"alias", "prefix-neighbor", "other-directory"} {
				if strings.Contains(preview, `id "`+id+`"`) {
					t.Fatalf("exact raw-command preview selected unrelated %q: %q", id, preview)
				}
			}
			if before, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(before) != 4 {
				t.Fatalf("preview replayed or changed history: %+v %v", before, err)
			}
			token := regexp.MustCompile(`confirmation ([0-9a-f]{64})`).FindStringSubmatch(preview)
			if len(token) != 2 {
				t.Fatalf("missing confirmation: %q", preview)
			}
			if err := cmd.Flags().Set("confirm", token[1]); err != nil {
				t.Fatal(err)
			}
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatal(err)
			}
			after, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(after) != 3 {
				t.Fatalf("exact deletion changed unrelated history: %+v %v", after, err)
			}
			for _, event := range after {
				if event.EventKey != "alias" && event.EventKey != "prefix-neighbor" && event.EventKey != "other-directory" {
					t.Fatalf("selected execution survived: %+v", event)
				}
			}
			if journal, err := previewHistoryRecoveryJournal(t.Context()); err != nil || len(journal) != 0 {
				t.Fatalf("confirmed pending execution survived journal cleanup: %+v %v", journal, err)
			}
		})
	}
}

func TestHistoryPrivacyForgetCommandPreviewsAndConfirmsExactTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index, command := range []string{"ssh forge@one", "ssh forge@one", "ssh forge@one-other"} {
		event := scoring.HistoryEvent{EventKey: []string{"first", "second", "keep"}[index], Command: command, Cwd: "/repo", Source: "vuja", StartedAt: at.Add(time.Duration(index) * time.Minute)}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	cmd, _, err := historyCmd.Find([]string{"forget"})
	if err != nil || cmd == historyCmd || cmd.RunE == nil {
		t.Fatal("history cannot preview/confirm selective forgetting")
	}
	oldOutput := cmd.OutOrStdout()
	var output bytes.Buffer
	cmd.SetOut(&output)
	t.Cleanup(func() {
		cmd.SetOut(oldOutput)
		for _, name := range []string{"id", "command", "cwd", "confirm"} {
			_ = cmd.Flags().Set(name, "")
		}
	})
	if err := cmd.Flags().Set("command", "ssh forge@one"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "matched 2 execution records") || strings.Contains(output.String(), "one-other") {
		t.Fatalf("preview must match exact command text, not prefixes: %q", output.String())
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 3 {
		t.Fatalf("preview mutated history: %+v %v", events, err)
	}
	token := regexp.MustCompile(`confirmation ([0-9a-f]{64})`).FindStringSubmatch(output.String())
	if len(token) != 2 {
		t.Fatalf("preview must provide a target-bound confirmation: %q", output.String())
	}
	if err := cmd.Flags().Set("confirm", "wrong-target-set"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("unrelated confirmation deleted history")
	}
	if err := cmd.Flags().Set("confirm", token[1]); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "forgotten 2 execution records") || !strings.Contains(output.String(), "output checkpoints unchanged") {
		t.Fatalf("confirmation must report its actual scope and separate output: %q", output.String())
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != "keep" {
		t.Fatalf("confirmed exact deletion changed unrelated history: %+v %v", events, err)
	}
}

// Goal QA scenario 3: delayed recovery must discard forgotten identities and
// continue to the following valid record without creating another fallback.
func TestHistoryPrivacyRecoveryDiscardsForgottenRecordsAndContinues(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := integration.HistoryEntry{ID: "forget-running", Command: "ssh forge@one", NormalizedCommand: "ssh forge@one", Cwd: "/repo", Source: "vuja", SessionID: "fixture", StartedAt: at, SubmittedAt: at, State: integration.HistoryStateRunning}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(forgotten)); err != nil {
		t.Fatal(err)
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, forgotten); err != nil {
		t.Fatal(err)
	}
	forgotten.HasExitCode, forgotten.State, forgotten.CompletedAt = true, integration.HistoryStateCompleted, at.Add(time.Second)
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, forgotten); err != nil {
		t.Fatal(err)
	}
	later := forgotten
	later.ID, later.Command, later.NormalizedCommand = "keep-later", "git status", "git status"
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, later); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.ID}); err != nil || removed != 1 {
		t.Fatalf("forget: removed=%d error=%v", removed, err)
	}
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatalf("forgotten recovery stopped valid later records: %v", err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != later.ID || events[0].State != integration.HistoryStateCompleted {
		t.Fatalf("recovery must retain only the independently expected later execution: %+v %v", events, err)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("discarded forgotten records must not remain pending: %v %v", info, err)
	}
}

func TestHistoryPrivacyForgottenSubmissionDoesNotCreateFallbackOrWarning(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := integration.HistoryEntry{ID: "forget-submission", Command: "git status", NormalizedCommand: "git status", Cwd: "/repo", Source: "vuja", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), State: integration.HistoryStateRunning}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	before := historyPersistenceFailureCount()
	observed := false
	persisted, journaled := persistHistorySubmissionDurablyObserved(store, entry, func(bool, bool) { observed = true })
	if persisted || journaled || observed {
		t.Fatalf("privacy rejection must not become persistence failure/fallback: persisted=%v journaled=%v observed=%v", persisted, journaled, observed)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("forgotten command was written to a recovery journal: %v", err)
	}
	if after := historyPersistenceFailureCount(); after != before {
		t.Fatalf("deliberate forgetting raised a history failure: before=%d after=%d", before, after)
	}
}

func TestHistoryPrivacyStalePublicationCannotRestoreForgottenExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	entry := integration.HistoryEntry{ID: "forget-stale-publisher", Command: "ssh forge@one", NormalizedCommand: "ssh forge@one", Cwd: "/repo", Source: "vuja", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), State: integration.HistoryStateRunning}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := publishCanonicalStoreHistory(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	publishHistorySubmission(store, entry)
	for _, candidate := range integration.RichHistorySnapshot() {
		if candidate.ID == entry.ID {
			t.Fatal("a stale submission/completion publisher restored a forgotten execution")
		}
	}
}

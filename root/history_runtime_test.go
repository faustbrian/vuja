package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"golang.org/x/sys/unix"
)

func TestCanonicalHistoryStoreRetriesAfterTransientInitializationFailure(t *testing.T) {
	transient := errors.New("database temporarily busy")
	calls := 0
	initialize := func(string) (*scoring.FrecencyStore, error) {
		calls++
		if calls == 1 {
			return nil, transient
		}
		return &scoring.FrecencyStore{}, nil
	}

	store, recovered, err := ensureCanonicalHistoryStore(nil, "session", initialize)
	if !errors.Is(err, transient) || store != nil || recovered {
		t.Fatalf("expected the transient initialization failure to remain retryable, store=%v recovered=%v err=%v", store, recovered, err)
	}
	store, recovered, err = ensureCanonicalHistoryStore(nil, "session", initialize)
	if err != nil || store == nil || !recovered {
		t.Fatalf("expected a later submission to recover the canonical store, store=%v recovered=%v err=%v", store, recovered, err)
	}
	if same, initialized, err := ensureCanonicalHistoryStore(store, "session", initialize); err != nil || same != store || initialized || calls != 2 {
		t.Fatalf("expected the recovered store to be reused, same=%v initialized=%v calls=%d err=%v", same == store, initialized, calls, err)
	}
}

func TestCanonicalHistoryRecallsAllVujaExecutionsAfterStoreReopen(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for index := range 30 {
		command := fmt.Sprintf("ssh forge@host-%02d", index)
		if err := store.RecordHistoryEvent(t.Context(), scoring.HistoryEvent{
			EventKey: fmt.Sprintf("vuja:ssh:%02d", index), Command: command, NormalizedCommand: command,
			Cwd: "/repo", SubmittedAt: now.Add(time.Duration(index) * time.Minute),
			StartedAt: now.Add(time.Duration(index) * time.Minute), Source: "vuja",
			State: "completed", ExitCode: 0, HasExitCode: true,
		}); err != nil {
			t.Fatal(err)
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
	if _, err := publishCanonicalStoreHistory(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}

	results, err := integration.SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 30 {
		t.Fatalf("expected all 30 Vuja-owned SSH executions after restart, got %d", len(results))
	}
}

func TestSubmittedLongRunningCommandAppearsBeforeCompletion(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{
		ID: "vuja:running-ssh", Command: "ssh forge@long-running", NormalizedCommand: "ssh forge@long-running",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja", State: integration.HistoryStateRunning,
	}

	if !recordHistorySubmission(store, entry) {
		t.Fatal("expected the submission to persist before the command completes")
	}
	results, err := integration.SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Cmd != entry.Command {
		t.Fatalf("expected the active SSH command in inline recall, got %+v", results)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != integration.HistoryStateRunning || events[0].HasExitCode {
		t.Fatalf("expected one unfinished durable event before completion, got %+v", events)
	}
}

func TestLiveHistorySessionIDsProtectsConcurrentLocalAndRemoteSessions(t *testing.T) {
	events := []scoring.HistoryEvent{
		{SessionID: "101-local", Host: "devbox"},
		{SessionID: "202-dead", Host: "devbox"},
		{SessionID: "303-remote", Host: "other-host"},
		{SessionID: "404-current", Host: "devbox"},
	}
	live := liveHistorySessionIDs(events, "404-current", "devbox", func(pid int) bool { return pid == 101 })
	if len(live) != 2 || live[0] != "101-local" || live[1] != "303-remote" {
		t.Fatalf("expected live local and unverifiable remote sessions to remain running, got %v", live)
	}
}

func TestParseHistoryPersistenceFailureCountSupportsLegacyAndAppendOnlyFormats(t *testing.T) {
	for name, test := range map[string]struct {
		data string
		want uint64
	}{
		"legacy counter": {data: "5\n", want: 5},
		"append only":    {data: "1\n1\n1\n", want: 3},
		"mixed upgrade":  {data: "5\n1\n1\n", want: 7},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := parseHistoryPersistenceFailureCount(test.data)
			if !ok || got != test.want {
				t.Fatalf("expected %d failures, got %d valid=%v", test.want, got, ok)
			}
		})
	}
	if _, ok := parseHistoryPersistenceFailureCount("corrupt\n"); ok {
		t.Fatal("expected malformed failure metadata to be rejected")
	}
}

func TestPersistenceFailureCounterCompactsLegacyAppendOnlyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history-write-failures")
	if err := os.WriteFile(path, []byte("5\n1\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := incrementHistoryPersistenceFailureFile(path); err != nil {
		t.Fatal(err)
	}
	if err := incrementHistoryPersistenceFailureFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "9\n" {
		t.Fatalf("expected one compact cross-process counter, got %q", got)
	}
}

func TestRecoveryJournalAppendIsBoundedWhenAnotherWindowOwnsTheLock(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsurePrivateDir(filepath.Dir(journalPath)); err != nil {
		t.Fatal(err)
	}
	owner, err := config.OpenPrivateFile(journalPath, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(owner.Fd()), unix.LOCK_UN) //nolint:errcheck -- best-effort test cleanup

	started := time.Now()
	err = appendHistoryRecoveryRecord(historyRecoverySubmission, integration.HistoryEntry{
		ID: "vuja:journal:contended", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		SubmittedAt: started, StartedAt: started, Source: "vuja", State: integration.HistoryStateRunning,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a bounded journal-lock deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("expected contention to return before shell execution stalls, took %s", elapsed)
	}
}

func TestCanonicalSnapshotPublicationInvalidatesDeletedRankingSignals(t *testing.T) {
	scoring.InvalidateSignalCache()
	t.Cleanup(scoring.InvalidateSignalCache)
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := t.TempDir()
	if err := store.Record(t.Context(), "ssh forge@removed", cwd, 0); err != nil {
		t.Fatal(err)
	}
	before := scoring.CollectSignals(t.Context(), cwd, "ssh", "ssh", store, "", "")
	if len(before.LocalFrecency) != 1 {
		t.Fatalf("expected the precondition to populate the signal cache, got %+v", before.LocalFrecency)
	}
	if err := store.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := publishCanonicalStoreHistory(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	after := scoring.CollectSignals(t.Context(), cwd, "ssh", "ssh", store, "", "")
	if len(after.LocalFrecency) != 0 || len(after.GlobalFrecency) != 0 {
		t.Fatalf("expected deletion publication to clear cached ranking signals, got local=%+v global=%+v", after.LocalFrecency, after.GlobalFrecency)
	}
}

func TestCanonicalPublicationLockDoesNotEncloseSubmissionPersistence(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	canonicalHistoryMutationMu.Lock()
	locked := true
	defer func() {
		if locked {
			canonicalHistoryMutationMu.Unlock()
		}
	}()

	entry := integration.HistoryEntry{
		ID: "vuja:bounded-lock", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		StartedAt: time.Now(), SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateRunning,
	}
	done := make(chan bool, 1)
	go func() { done <- recordHistorySubmission(store, entry) }()

	deadline := time.Now().Add(time.Second)
	persisted := false
	for time.Now().Before(deadline) {
		events, queryErr := store.QueryAllHistoryEvents(t.Context())
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if len(events) == 1 && events[0].EventKey == entry.ID {
			persisted = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !persisted {
		t.Fatal("expected durable submission before the contended publication boundary")
	}
	canonicalHistoryMutationMu.Unlock()
	locked = false
	if !<-done {
		t.Fatal("expected submission publication to finish after contention cleared")
	}
}

func TestSubmissionPersistenceCanAcknowledgeWhileCanonicalPublicationIsBusy(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := integration.HistoryEntry{
		ID: "vuja:ack-before-publication", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		StartedAt: time.Now(), SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateRunning,
	}

	canonicalHistoryMutationMu.Lock()
	defer canonicalHistoryMutationMu.Unlock()
	done := make(chan bool, 1)
	go func() { done <- persistHistorySubmission(store, entry) }()
	select {
	case persisted := <-done:
		if !persisted {
			t.Fatal("expected the durable submission to succeed while publication was busy")
		}
	case <-time.After(time.Second):
		t.Fatal("durable submission was incorrectly blocked by canonical publication")
	}
}

func TestSubmissionPersistenceFallsBackToDurableRecoveryJournal(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{
		ID: "vuja:journal:submission", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja",
		SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
	}

	persisted, journaled := persistHistorySubmissionDurably(nil, entry)
	if !persisted || !journaled {
		t.Fatalf("expected owner-only recovery journal fallback, persisted=%t journaled=%t", persisted, journaled)
	}
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected owner-only recovery journal, got %04o", info.Mode().Perm())
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatalf("expected drained journal replay to remain idempotent: %v", err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != entry.ID || events[0].State != integration.HistoryStateRunning {
		t.Fatalf("expected fallback submission in canonical history, got %+v", events)
	}
	info, err = os.Stat(journalPath)
	if err != nil || info.Size() != 0 {
		t.Fatalf("expected replayed journal to be durably drained, info=%v err=%v", info, err)
	}
}

func TestRecoveryJournalRejectsPrivateCommandsBeforePersistence(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	entry := integration.HistoryEntry{
		ID: "vuja:journal:private", Command: " ssh forge@private", NormalizedCommand: "ssh forge@private",
		StartedAt: time.Now(), SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateRunning,
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err == nil {
		t.Fatal("expected leading-space command to be rejected before recovery persistence")
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rejected command not to create a recovery journal, got %v", err)
	}
}

func TestRecoveryJournalAppendSeparatesAnUnacknowledgedPartialTail(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsurePrivateDir(filepath.Dir(journalPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, []byte(`{"kind":"submission","entry":`), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := integration.HistoryEntry{
		ID: "vuja:journal:after-partial", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: time.Now(), StartedAt: time.Now(), Source: "vuja",
		SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != entry.ID {
		t.Fatalf("expected the complete record after a partial tail to recover, got %+v", events)
	}
}

func TestRecoveryJournalReplaysCompletionIntoTheSubmittedEvent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{
		ID: "vuja:journal:completion", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja",
		SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	entry.CompletedAt = started.Add(5 * time.Second)
	entry.Duration = 5 * time.Second
	entry.ExitCode = 130
	entry.HasExitCode = true
	entry.State = integration.HistoryStateFailed
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, entry); err != nil {
		t.Fatal(err)
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != entry.ID || events[0].State != integration.HistoryStateFailed ||
		!events[0].HasExitCode || events[0].ExitCode != 130 || events[0].Duration != 5*time.Second {
		t.Fatalf("expected journal completion to enrich one canonical event, got %+v", events)
	}
}

func TestPersistedSubmissionCompletionFallsBackToTheRecoveryJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	databasePath := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{
		ID: "vuja:journal:persisted-completion", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja",
		SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
	}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	entry.CompletedAt = started.Add(5 * time.Second)
	entry.Duration = 5 * time.Second
	entry.ExitCode = 130
	entry.HasExitCode = true
	entry.State = integration.HistoryStateFailed
	completed, journaled := completeHistoryEventDurably(store, entry)
	if completed || !journaled {
		t.Fatalf("expected failed SQLite completion to be secured for recovery, completed=%t journaled=%t", completed, journaled)
	}

	reopened, err := scoring.NewFrecencyStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := retryHistoryRecoveryOnce(t.Context(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("expected the live-session retry to recover the journaled completion")
	}
	events, err := reopened.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != entry.ID || events[0].State != integration.HistoryStateFailed ||
		!events[0].HasExitCode || events[0].ExitCode != 130 || events[0].Duration != 5*time.Second {
		t.Fatalf("expected fallback completion to enrich the persisted submission, got %+v", events)
	}
}

func TestRecoveryJournalReplayCheckpointsWithoutLosingLaterRecords(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for index := range 3 {
		command := fmt.Sprintf("ssh forge@host-%d", index)
		if err := appendHistoryRecoveryRecord(historyRecoverySubmission, integration.HistoryEntry{
			ID: fmt.Sprintf("vuja:journal:checkpoint:%d", index), Command: command, NormalizedCommand: command,
			Cwd: "/repo", SubmittedAt: started.Add(time.Duration(index) * time.Second),
			StartedAt: started.Add(time.Duration(index) * time.Second), Source: "vuja",
			SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
		}); err != nil {
			t.Fatal(err)
		}
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	drained, err := replayHistoryRecoveryJournalBatch(t.Context(), store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if drained {
		t.Fatal("expected the bounded replay to leave a durable checkpoint")
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one checkpointed event after the bounded replay, got %+v", events)
	}

	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	events, err = store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected the next replay to resume and drain all events, got %+v", events)
	}
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journalPath)
	if err != nil || info.Size() != 0 {
		t.Fatalf("expected checkpointed replay to drain the journal, info=%v err=%v", info, err)
	}
}

func TestRecoveryJournalReplaySkipsOversizedRecordAndRecoversLaterRecords(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsurePrivateDir(filepath.Dir(journalPath)); err != nil {
		t.Fatal(err)
	}
	journal, err := config.OpenPrivateFile(journalPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHistoryRecoveryBytes(journal, bytes.Repeat([]byte("x"), historyRecoveryRecordLimit+1)); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := writeHistoryRecoveryBytes(journal, []byte{'\n'}); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := writeHistoryRecoveryBytes(journal, []byte("{\"kind\":\"completion\",\"entry\":{\"id\":\"invalid-completion\",\"command\":\"ssh forge@invalid\",\"source\":\"vuja\"}}\n")); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	entry := integration.HistoryEntry{
		ID: "vuja:journal:after-corruption", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: time.Now(), StartedAt: time.Now(), Source: "vuja",
		SessionID: "session-a", Shell: "zsh", State: integration.HistoryStateRunning,
	}
	payload, err := json.Marshal(historyRecoveryRecord{Kind: historyRecoverySubmission, Entry: entry})
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := writeHistoryRecoveryBytes(journal, append(payload, '\n')); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != entry.ID {
		t.Fatalf("expected valid history after corrupt input to recover, got %+v", events)
	}
	info, err := os.Stat(journalPath)
	if err != nil || info.Size() != 0 {
		t.Fatalf("expected replay to drain corrupt journal, info=%v err=%v", info, err)
	}
}

package root

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
	"golang.org/x/sys/unix"
)

var historyPersistenceFailures atomic.Uint64
var canonicalHistoryMutationMu sync.Mutex

const (
	historyRecoverySubmission   = "submission"
	historyRecoveryCompletion   = "completion"
	historyRecoveryRecordLimit  = 8 * 1024 * 1024
	historyRecoveryCheckpoint   = 256
	historyRecoveryLockTimeout  = 250 * time.Millisecond
	historyFailureLockTimeout   = 25 * time.Millisecond
	historyFileLockPollInterval = 5 * time.Millisecond
)

type historyRecoveryRecord struct {
	Kind  string                   `json:"kind"`
	Entry integration.HistoryEntry `json:"entry"`
}

type canonicalHistoryInitializer func(string) (*scoring.FrecencyStore, error)

func ensureCanonicalHistoryStore(
	current *scoring.FrecencyStore,
	sessionID string,
	initialize canonicalHistoryInitializer,
) (*scoring.FrecencyStore, bool, error) {
	if current != nil {
		return current, false, nil
	}
	store, err := initialize(sessionID)
	if err != nil {
		return nil, false, err
	}
	return store, true, nil
}

func recordHistoryPersistenceFailure() {
	historyPersistenceFailures.Add(1)
	path, err := historyPersistenceFailurePath()
	if err != nil {
		return
	}
	_ = incrementHistoryPersistenceFailureFile(path)
}

func acquireHistoryFileLock(ctx context.Context, file *os.File, operation int) error {
	if file == nil {
		return errors.New("history file lock requires an open file")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(historyFileLockPollInterval)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("history file lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func incrementHistoryPersistenceFailureFile(path string) error {
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), historyFailureLockTimeout)
	defer cancel()
	if err := acquireHistoryFileLock(ctx, file, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	count, valid, err := scanHistoryPersistenceFailureCount(file)
	if err != nil {
		return err
	}
	if !valid {
		count = 0
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.WriteString(strconv.FormatUint(count+1, 10) + "\n"); err != nil {
		return err
	}
	return file.Sync()
}

func historyPersistenceFailureCount() uint64 {
	path, err := historyPersistenceFailurePath()
	if err == nil {
		if file, openErr := os.Open(path); openErr == nil {
			defer file.Close()
			ctx, cancel := context.WithTimeout(context.Background(), historyFailureLockTimeout)
			if lockErr := acquireHistoryFileLock(ctx, file, unix.LOCK_SH); lockErr == nil {
				count, valid, scanErr := scanHistoryPersistenceFailureCount(file)
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				cancel()
				if scanErr == nil && valid {
					return count
				}
			} else {
				cancel()
			}
		}
	}
	return historyPersistenceFailures.Load()
}

func scanHistoryPersistenceFailureCount(reader io.Reader) (uint64, bool, error) {
	if reader == nil {
		return 0, false, nil
	}
	scanner := bufio.NewScanner(reader)
	var total uint64
	seen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count, err := strconv.ParseUint(line, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("invalid history persistence failure counter: %w", err)
		}
		total += count
		seen = true
	}
	if err := scanner.Err(); err != nil {
		return 0, false, err
	}
	return total, seen, nil
}

func parseHistoryPersistenceFailureCount(data string) (uint64, bool) {
	lines := strings.Fields(data)
	if len(lines) == 0 {
		return 0, false
	}
	var total uint64
	for _, line := range lines {
		count, err := strconv.ParseUint(line, 10, 64)
		if err != nil {
			return 0, false
		}
		total += count
	}
	return total, true
}

func historyPersistenceFailurePath() (string, error) {
	statePath, err := config.StatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), "history-write-failures"), nil
}

func historyRecoveryJournalPath() (string, error) {
	statePath, err := config.StatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), "history-recovery.jsonl"), nil
}

func historyRecoveryOffsetPath() (string, error) {
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return "", err
	}
	return path + ".offset", nil
}

func appendHistoryRecoveryRecord(kind string, entry integration.HistoryEntry) error {
	if kind != historyRecoverySubmission && kind != historyRecoveryCompletion {
		return fmt.Errorf("unsupported history recovery record kind %q", kind)
	}
	if strings.TrimSpace(entry.ID) == "" {
		return errors.New("history recovery event ID is required")
	}
	if _, recordable := policy.HistoryCommand(entry.Command, false); !recordable {
		return errors.New("history recovery event rejected by privacy policy")
	}
	normalized := strings.TrimSpace(entry.NormalizedCommand)
	if normalized == "" {
		normalized = entry.Command
	}
	if _, recordable := policy.HistoryCommand(normalized, false); !recordable {
		return errors.New("history recovery event rejected by privacy policy")
	}
	payload, err := json.Marshal(historyRecoveryRecord{Kind: kind, Entry: entry})
	if err != nil {
		return err
	}
	if len(payload) > historyRecoveryRecordLimit {
		return fmt.Errorf("history recovery record exceeds %d bytes", historyRecoveryRecordLimit)
	}
	payload = append(payload, '\n')
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return err
	}
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND)
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), historyRecoveryLockTimeout)
	defer cancel()
	if err := acquireHistoryFileLock(ctx, file, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		if err := writeHistoryRecoveryOffset(0); err != nil {
			return err
		}
	} else {
		var tail [1]byte
		if _, err := file.ReadAt(tail[:], info.Size()-1); err != nil {
			return err
		}
		if tail[0] != '\n' {
			// A previous process can exit between writing and syncing a record.
			// Terminate that unacknowledged partial line so this complete record
			// remains independently recoverable.
			if err := writeHistoryRecoveryBytes(file, []byte{'\n'}); err != nil {
				return err
			}
		}
	}
	if err := writeHistoryRecoveryBytes(file, payload); err != nil {
		return err
	}
	return file.Sync()
}

func replayHistoryRecoveryJournal(store *scoring.FrecencyStore) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := replayHistoryRecoveryJournalBatch(ctx, store, 0)
	return err
}

func replayHistoryRecoveryJournalBatch(ctx context.Context, store *scoring.FrecencyStore, maxRecords int) (bool, error) {
	if store == nil {
		return true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return false, err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := acquireHistoryFileLock(ctx, file, unix.LOCK_EX); err != nil {
		return false, err
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	journalSize := info.Size()
	offset, offsetPresent, err := readHistoryRecoveryOffset()
	if err != nil {
		return false, err
	}
	if offset < 0 || offset > journalSize {
		offset = 0
		offsetPresent = false
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}
	if !offsetPresent && offset != 0 {
		return false, errors.New("invalid history recovery offset")
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	malformed := 0
	processedOffset := offset
	processedRecords := 0
	reportMalformed := func() {
		if malformed == 0 {
			return
		}
		recordHistoryPersistenceFailure()
		logger.Errorf("discarded %d malformed or policy-rejected history recovery records", malformed)
		malformed = 0
	}
	checkpoint := func() error {
		if err := writeHistoryRecoveryOffset(processedOffset); err != nil {
			return err
		}
		reportMalformed()
		return nil
	}
	for {
		line, consumed, oversized, readErr := readBoundedHistoryRecoveryLine(reader, historyRecoveryRecordLimit)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			checkpointErr := checkpoint()
			return false, errors.Join(readErr, checkpointErr)
		}
		nextOffset := min(processedOffset+consumed, journalSize)
		if oversized {
			malformed++
		} else if len(line) > 0 {
			var record historyRecoveryRecord
			if json.Unmarshal(line, &record) != nil || !validHistoryRecoveryRecord(record) {
				malformed++
			} else if err := replayHistoryRecoveryRecord(ctx, store, record); err != nil {
				checkpointErr := checkpoint()
				return false, errors.Join(err, checkpointErr)
			}
		}
		processedOffset = nextOffset
		processedRecords++
		if processedRecords%historyRecoveryCheckpoint == 0 {
			if err := checkpoint(); err != nil {
				return false, err
			}
		}
		if maxRecords > 0 && processedRecords >= maxRecords {
			if processedOffset >= journalSize {
				break
			}
			if err := checkpoint(); err != nil {
				return false, err
			}
			return false, nil
		}
	}
	reportMalformed()
	// Reset the checkpoint durably before truncation. If the process exits
	// between these writes, a later replay can only repeat idempotent records;
	// it can never skip newly appended records based on a stale offset.
	if err := writeHistoryRecoveryOffset(0); err != nil {
		return false, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if err := file.Truncate(0); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

// readBoundedHistoryRecoveryLine consumes one complete journal record without
// ever allocating more than limit bytes for it. Oversized or corrupted lines
// are discarded through their newline so replay can checkpoint past them and
// continue recovering later valid records.
func readBoundedHistoryRecoveryLine(reader *bufio.Reader, limit int) ([]byte, int64, bool, error) {
	if reader == nil {
		return nil, 0, false, io.EOF
	}
	if limit < 0 {
		limit = 0
	}
	line := make([]byte, 0, min(limit, 64*1024))
	var consumed int64
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		content := fragment
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		if !oversized {
			if len(content) > limit-len(line) {
				line = nil
				oversized = true
			} else {
				line = append(line, content...)
			}
		}
		switch {
		case err == nil:
			return line, consumed, oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if consumed == 0 {
				return nil, 0, false, io.EOF
			}
			return line, consumed, oversized, nil
		default:
			return nil, consumed, oversized, err
		}
	}
}

func replayHistoryRecoveryRecord(ctx context.Context, store *scoring.FrecencyStore, record historyRecoveryRecord) error {
	existing, err := store.QueryHistoryEventsByKeys(ctx, []string{record.Entry.ID})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		submission := record.Entry
		submission.CompletedAt = time.Time{}
		submission.Duration = 0
		submission.ExitCode = 0
		submission.HasExitCode = false
		submission.State = integration.HistoryStateRunning
		if err := store.RecordHistorySubmission(ctx, scoringHistoryEvent(submission)); err != nil {
			return err
		}
	}
	if record.Kind == historyRecoveryCompletion {
		_, err = store.CompleteHistoryEvent(ctx, scoringHistoryEvent(record.Entry))
	}
	return err
}

func validHistoryRecoveryRecord(record historyRecoveryRecord) bool {
	if record.Kind != historyRecoverySubmission && record.Kind != historyRecoveryCompletion ||
		strings.TrimSpace(record.Entry.ID) == "" || strings.TrimSpace(record.Entry.Source) == "" {
		return false
	}
	if record.Kind == historyRecoveryCompletion && !record.Entry.HasExitCode {
		return false
	}
	if _, ok := policy.HistoryCommand(record.Entry.Command, false); !ok {
		return false
	}
	normalized := strings.TrimSpace(record.Entry.NormalizedCommand)
	if normalized == "" {
		normalized = record.Entry.Command
	}
	_, ok := policy.HistoryCommand(normalized, false)
	return ok
}

func writeHistoryRecoveryBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readHistoryRecoveryOffset() (int64, bool, error) {
	path, err := historyRecoveryOffsetPath()
	if err != nil {
		return 0, false, err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return 0, false, err
	}
	if len(data) > 64 {
		return 0, false, nil
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || offset < 0 {
		return 0, false, nil
	}
	return offset, true, nil
}

func writeHistoryRecoveryOffset(offset int64) error {
	if offset < 0 {
		return errors.New("history recovery offset cannot be negative")
	}
	path, err := historyRecoveryOffsetPath()
	if err != nil {
		return err
	}
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if err := writeHistoryRecoveryBytes(file, []byte(strconv.FormatInt(offset, 10)+"\n")); err != nil {
		return err
	}
	return file.Sync()
}

func clearHistoryRecoveryJournal() error {
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return err
	}
	file, err := config.OpenPrivateFile(path, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return writeHistoryRecoveryOffset(0)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), historyRecoveryLockTimeout)
	defer cancel()
	if err := acquireHistoryFileLock(ctx, file, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	if err := writeHistoryRecoveryOffset(0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

func recordHistoryDerivedFailure(kind string, err error) {
	if err == nil {
		return
	}
	recordHistoryPersistenceFailure()
	logger.Errorf("failed to persist history %s metadata: %v", kind, err)
}

func initializeCanonicalHistory(sessionID string) (*scoring.FrecencyStore, error) {
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		return nil, err
	}
	store.SetHistoryChangeOrigin(sessionID)
	if err := replayHistoryRecoveryJournal(store); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unfinished, err := store.QueryUnfinishedHistoryEvents(ctx)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	otherLiveSessions := liveHistorySessionIDs(unfinished, sessionID, host, historyProcessAlive)
	if _, err := store.ReconcileInterruptedHistory(ctx, sessionID, otherLiveSessions...); err != nil {
		return nil, err
	}
	cfg := config.Get().History
	if cfg.Retention == "bounded" && cfg.MaxEvents > 0 {
		if _, err := store.PruneHistoryEvents(ctx, cfg.MaxEvents); err != nil {
			return nil, err
		}
	}
	entries, err := publishCanonicalStoreHistory(ctx, store)
	if err != nil {
		return nil, err
	}
	historyDirectories := historyNavigationDirectoryImports(entries, time.Now())
	if err := store.ReplaceDirectorySource(ctx, "history", historyDirectories); err != nil {
		recordHistoryDerivedFailure("directory source", err)
	}
	zoxideDirectories := loadOptionalZoxideDirectories(ctx, config.Get().Suggestions.ImportZoxide, func(parent context.Context) []scoring.DirectoryImport {
		zoxideCtx, zoxideCancel := context.WithTimeout(parent, 500*time.Millisecond)
		defer zoxideCancel()
		return loadZoxideDirectories(zoxideCtx)
	})
	if err := store.ReplaceDirectorySource(ctx, "zoxide", zoxideDirectories); err != nil {
		recordHistoryDerivedFailure("optional zoxide source", err)
	}
	if err := store.ReplaceDirectorySource(ctx, "git-worktrees", gitWorktreeImports(spec.GetCWD(), historyDirectories)); err != nil {
		recordHistoryDerivedFailure("git worktree source", err)
	}
	return store, nil
}

func liveHistorySessionIDs(events []scoring.HistoryEvent, activeSessionID, host string, processAlive func(int) bool) []string {
	if processAlive == nil {
		return nil
	}
	seen := make(map[string]bool)
	var sessions []string
	for _, event := range events {
		sessionID := strings.TrimSpace(event.SessionID)
		if sessionID == "" || sessionID == activeSessionID || seen[sessionID] {
			continue
		}
		if strings.TrimSpace(event.Host) != "" && strings.TrimSpace(host) != "" && event.Host != host {
			seen[sessionID] = true
			sessions = append(sessions, sessionID)
			continue
		}
		pidText, _, ok := strings.Cut(sessionID, "-")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil || pid <= 0 || !processAlive(pid) {
			continue
		}
		seen[sessionID] = true
		sessions = append(sessions, sessionID)
	}
	return sessions
}

func historyProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer process.Release()
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func publishCanonicalStoreHistory(ctx context.Context, store *scoring.FrecencyStore) ([]integration.HistoryEntry, error) {
	canonicalHistoryMutationMu.Lock()
	defer canonicalHistoryMutationMu.Unlock()
	return publishCanonicalStoreHistoryLocked(ctx, store)
}

func publishCanonicalStoreHistoryLocked(ctx context.Context, store *scoring.FrecencyStore) ([]integration.HistoryEntry, error) {
	changeBoundary, err := store.QueryHistoryChanges(ctx, 0, 1)
	if err != nil {
		return nil, err
	}
	events, err := store.QueryAllHistoryEvents(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]integration.HistoryEntry, 0, len(events))
	for _, event := range events {
		command := strings.TrimSpace(event.NormalizedCommand)
		if command == "" {
			command = strings.TrimSpace(event.Command)
		}
		if policy.IsSensitive(event.Command) || policy.IsSensitive(command) {
			continue
		}
		entries = append(entries, historyEventEntry(event))
	}
	integration.PublishCanonicalHistory(entries)
	store.SetHistorySnapshotSequence(changeBoundary.Latest)
	scoring.InvalidateSignalCache()
	spec.NotifyCompletionUpdate()
	return entries, nil
}

func isHistoryMutationCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 3 || filepath.Base(fields[0]) != "vuja" || fields[1] != "history" {
		return false
	}
	return fields[2] == "clear" || fields[2] == "prune" || fields[2] == "import"
}

func isHistoryClearCommand(command string) bool {
	fields := strings.Fields(command)
	return len(fields) >= 3 && filepath.Base(fields[0]) == "vuja" && fields[1] == "history" && fields[2] == "clear"
}

func historyPruneLimit(command string) (int, bool) {
	fields := strings.Fields(command)
	if len(fields) < 3 || filepath.Base(fields[0]) != "vuja" || fields[1] != "history" || fields[2] != "prune" {
		return 0, false
	}
	for index := 3; index < len(fields); index++ {
		value := ""
		if fields[index] == "--max" && index+1 < len(fields) {
			value = fields[index+1]
		} else if after, ok := strings.CutPrefix(fields[index], "--max="); ok {
			value = after
		}
		if limit, err := strconv.Atoi(value); err == nil && limit > 0 {
			return limit, true
		}
	}
	return 0, false
}

func historyEventEntry(event scoring.HistoryEvent) integration.HistoryEntry {
	return integration.HistoryEntry{
		ID:                event.EventKey,
		Command:           event.Command,
		NormalizedCommand: event.NormalizedCommand,
		Cwd:               event.Cwd,
		SubmittedAt:       event.SubmittedAt,
		StartedAt:         event.StartedAt,
		CompletedAt:       event.CompletedAt,
		Duration:          event.Duration,
		ExitCode:          event.ExitCode,
		HasExitCode:       event.HasExitCode,
		Source:            event.Source,
		Host:              event.Host,
		SessionID:         event.SessionID,
		Shell:             event.Shell,
		State:             event.State,
		HistoryOrder:      event.HistoryOrder,
		Occurrences:       event.Occurrences,
	}
}

func scoringHistoryEvent(entry integration.HistoryEntry) scoring.HistoryEvent {
	return scoring.HistoryEvent{
		EventKey:          entry.ID,
		Command:           entry.Command,
		NormalizedCommand: entry.NormalizedCommand,
		Cwd:               entry.Cwd,
		SubmittedAt:       entry.SubmittedAt,
		StartedAt:         entry.StartedAt,
		CompletedAt:       entry.CompletedAt,
		Duration:          entry.Duration,
		ExitCode:          entry.ExitCode,
		HasExitCode:       entry.HasExitCode,
		Source:            entry.Source,
		Host:              entry.Host,
		SessionID:         entry.SessionID,
		Shell:             entry.Shell,
		State:             entry.State,
		HistoryOrder:      entry.HistoryOrder,
		Occurrences:       entry.Occurrences,
	}
}

func recordHistorySubmission(store *scoring.FrecencyStore, entry integration.HistoryEntry) bool {
	if !persistHistorySubmission(store, entry) {
		return false
	}
	publishHistorySubmission(entry)
	return true
}

func persistHistorySubmission(store *scoring.FrecencyStore, entry integration.HistoryEntry) bool {
	if store == nil {
		recordHistoryPersistenceFailure()
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.RecordHistorySubmission(ctx, scoringHistoryEvent(entry)); err != nil {
		recordHistoryPersistenceFailure()
		logger.Errorf("failed to persist submitted history event: %v", err)
		return false
	}
	return true
}

func persistHistorySubmissionDurably(store *scoring.FrecencyStore, entry integration.HistoryEntry) (persisted, journaled bool) {
	if persistHistorySubmission(store, entry) {
		return true, false
	}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		recordHistoryPersistenceFailure()
		logger.Errorf("failed to persist history recovery submission: %v", err)
		return false, false
	}
	return true, true
}

func publishHistorySubmission(entry integration.HistoryEntry) {
	canonicalHistoryMutationMu.Lock()
	integration.PublishCanonicalHistoryEntry(entry)
	canonicalHistoryMutationMu.Unlock()
}

func completeHistoryEvent(store *scoring.FrecencyStore, entry integration.HistoryEntry) (bool, error) {
	if store == nil {
		recordHistoryPersistenceFailure()
		return false, errors.New("canonical history store is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	completed, err := store.CompleteHistoryEvent(ctx, scoringHistoryEvent(entry))
	if err != nil {
		recordHistoryPersistenceFailure()
		logger.Errorf("failed to complete history event: %v", err)
		return false, err
	}
	if !completed {
		return false, nil
	}
	cfg := config.Get().History
	if cfg.Retention == "bounded" && cfg.MaxEvents > 0 {
		retentionCtx, retentionCancel := context.WithTimeout(context.Background(), 5*time.Second)
		removed, err := store.PruneHistoryEvents(retentionCtx, cfg.MaxEvents)
		if err != nil {
			recordHistoryDerivedFailure("retention", err)
		} else if removed > 0 {
			if _, err := publishCanonicalStoreHistory(retentionCtx, store); err != nil {
				recordHistoryDerivedFailure("retention snapshot", err)
			}
			retentionCancel()
			return true, nil
		}
		retentionCancel()
	}
	canonicalHistoryMutationMu.Lock()
	integration.PublishCanonicalHistoryEntry(entry)
	canonicalHistoryMutationMu.Unlock()
	return true, nil
}

// completeHistoryEventDurably preserves completion metadata when SQLite is
// temporarily unavailable after the submission was already committed. The
// recovery record is fsynced before success is reported to the caller and is
// replayed idempotently into that same event on the next recovery boundary.
func completeHistoryEventDurably(
	store *scoring.FrecencyStore,
	entry integration.HistoryEntry,
) (completed, journaled bool) {
	completed, err := completeHistoryEvent(store, entry)
	if err == nil {
		// A repeated completion is an idempotent no-op and does not need a
		// recovery record.
		return completed, false
	}
	if completed {
		return true, false
	}
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, entry); err != nil {
		recordHistoryPersistenceFailure()
		logger.Errorf("failed to persist history recovery completion: %v", err)
		return false, false
	}
	return false, true
}

package root

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func newHistoryForgetCommand() *cobra.Command {
	var id, command, cwd, confirmation string
	cmd := &cobra.Command{
		Use: "forget", Args: cobra.NoArgs,
		Short: "preview and confirm forgetting specific recorded executions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Unlike existing maintenance commands, preview must not replay a
			// pending journal and thereby mutate history before confirmation.
			store, err := scoring.GetFrecencyStore()
			if err != nil {
				return err
			}
			parent := cmd.Context()
			if parent == nil {
				parent = context.Background()
			}
			ctx, cancel := context.WithTimeout(parent, 10*time.Second)
			defer cancel()
			events, pending, err := queryHistoryForgetSelection(ctx, store, id, command, cwd)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "matched 0 execution records; nothing forgotten")
				return nil
			}
			token := historyForgetConfirmation(events)
			if confirmation == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "matched %d execution records\n", len(events))
				for _, event := range events[:min(len(events), 20)] {
					fmt.Fprintf(cmd.OutOrStdout(), "id %q command %q directory %q state %s occurrences %d\n", event.EventKey, event.Command, event.Cwd, event.State, max(event.Occurrences, 1))
				}
				if len(events) > 20 {
					fmt.Fprintf(cmd.OutOrStdout(), "%d more matched executions\n", len(events)-20)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "confirmation %s\nrepeat the same selection with --confirm %s\n", token, token)
				fmt.Fprintln(cmd.OutOrStdout(), "output checkpoints unchanged; external history files and saved exports are separate")
				for _, event := range events {
					if !event.Imported {
						fmt.Fprintln(cmd.OutOrStdout(), "unattributed legacy learning cannot be assigned to one execution; removal is refused if it cannot safely preserve unrelated learning")
						break
					}
				}
				for _, event := range events {
					if event.Imported {
						fmt.Fprintln(cmd.OutOrStdout(), "identical imported source provenance stays excluded on refresh; known-time cross-source copies follow the one-to-one matching rule; missing timestamps cannot distinguish new external copies; new Vuja executions remain allowed")
						break
					}
				}
				return nil
			}
			if !strings.EqualFold(confirmation, token) {
				return fmt.Errorf("confirmation does not match the current execution set; preview again")
			}
			return forgetSelectedHistory(ctx, store, events, pending, cmd)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "exact execution ID to forget")
	cmd.Flags().StringVar(&command, "command", "", "exact recorded command text to forget (not a pattern)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "restrict command matches to this exact directory")
	cmd.Flags().StringVar(&confirmation, "confirm", "", "confirmation token from previewing this execution set")
	return cmd
}

func historyForgetConfirmation(events []scoring.HistoryEvent) string {
	ordered := append([]scoring.HistoryEvent(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].EventKey < ordered[j].EventKey })
	hash := sha256.New()
	for _, event := range ordered {
		// Completion may arrive between preview and confirmation. Bind the
		// immutable identity instead, so a reused ID cannot change the target.
		for _, field := range []string{event.EventKey, strings.TrimSpace(event.Command), event.Cwd, event.Source, event.StartedAt.UTC().Format(time.RFC3339Nano)} {
			_, _ = fmt.Fprintf(hash, "%d:%s\n", len(field), field)
		}
		_, _ = fmt.Fprintf(hash, "%t\n", event.Imported)
		_, _ = fmt.Fprintf(hash, "%d\n", max(event.Occurrences, 1))
		_, _ = fmt.Fprintf(hash, "%t\n", event.Aggregate || event.Occurrences > 1)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func forgetSelectedHistory(ctx context.Context, store *scoring.FrecencyStore, confirmed, pending []scoring.HistoryEvent, cmd *cobra.Command) error {
	canonicalHistoryMutationMu.Lock()
	defer canonicalHistoryMutationMu.Unlock()
	removed, err := store.ForgetConfirmedHistoryEvents(ctx, confirmed, pending)
	if err != nil {
		return err
	}
	// Use the existing authoritative recovery owner. It discards excluded IDs
	// and durably preserves unrelated pending records before truncating them.
	if err := replayHistoryRecoveryJournal(store); err != nil {
		return fmt.Errorf("history was forgotten, but recovery-journal cleanup is pending: %w", err)
	}
	_, err = publishCanonicalStoreHistoryLocked(ctx, store)
	if err != nil {
		return fmt.Errorf("history was forgotten, but recall refresh is pending: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "forgotten %d execution records; output checkpoints unchanged\n", removed)
	return nil
}

func queryHistoryForgetSelection(ctx context.Context, store *scoring.FrecencyStore, id, command, cwd string) ([]scoring.HistoryEvent, []scoring.HistoryEvent, error) {
	events, err := store.QueryHistoryForgetMatches(ctx, id, command, cwd)
	if err != nil {
		return nil, nil, err
	}
	journal, err := previewHistoryRecoveryJournal(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(journal))
	for key := range journal {
		keys = append(keys, key)
	}
	// Canonical data wins even when it no longer matches the selector. Do not
	// let an old recovery payload select a different execution under that ID.
	for start := 0; start < len(keys); start += 128 {
		canonical, err := store.QueryHistoryEventsByKeys(ctx, keys[start:min(start+128, len(keys))])
		if err != nil {
			return nil, nil, err
		}
		for _, event := range canonical {
			delete(journal, event.EventKey)
		}
	}
	var pending []scoring.HistoryEvent
	for _, event := range journal {
		matches := strings.TrimSpace(id) == event.EventKey
		if strings.TrimSpace(command) != "" {
			matches = strings.TrimSpace(event.Command) == strings.TrimSpace(command) && (cwd == "" || event.Cwd == cwd)
		}
		if !matches {
			continue
		}
		forgotten, err := store.HistoryEventForgotten(ctx, event.EventKey)
		if err != nil {
			return nil, nil, err
		}
		if !forgotten {
			pending = append(pending, event)
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].EventKey < events[j].EventKey })
	return events, pending, nil
}

// Preview takes a bounded read-only snapshot under the existing journal lock.
// It never repairs permissions, replays, checkpoints, or creates a file.
func previewHistoryRecoveryJournal(ctx context.Context) (map[string]scoring.HistoryEvent, error) {
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return nil, err
	}
	file, err := openHistoryPreviewFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := acquireHistoryFileLock(ctx, file, unix.LOCK_SH); err != nil {
		return nil, err
	}
	data, err := func() ([]byte, error) {
		defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		var offset int64
		offsetFile, err := openHistoryPreviewFile(path + ".offset")
		if err == nil {
			value, readErr := io.ReadAll(io.LimitReader(offsetFile, 65))
			_ = offsetFile.Close()
			if readErr != nil {
				return nil, readErr
			}
			offset, _, err = parseHistoryRecoveryOffset(value)
		} else if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if offset > info.Size() {
			offset = 0
		}
		const previewLimit = 64 * 1024 * 1024
		if info.Size()-offset > previewLimit {
			return nil, errors.New("pending history exceeds the preview limit; restore history persistence before previewing")
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(file, info.Size()-offset))
	}()
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(bytes.NewReader(data), 64*1024)
	result := make(map[string]scoring.HistoryEvent)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, _, oversized, err := readBoundedHistoryRecoveryLine(reader, historyRecoveryRecordLimit)
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		var record historyRecoveryRecord
		if oversized || json.Unmarshal(line, &record) != nil || !validHistoryRecoveryRecord(record) {
			continue // Same malformed/policy-rejected records that replay skips.
		}
		event := scoringHistoryEvent(record.Entry)
		if previous, exists := result[event.EventKey]; exists && historyForgetConfirmation([]scoring.HistoryEvent{previous}) != historyForgetConfirmation([]scoring.HistoryEvent{event}) {
			return nil, errors.New("pending history contains conflicting execution identities; preview cannot safely select them")
		}
		if len(result) >= 10000 {
			if _, exists := result[event.EventKey]; !exists {
				return nil, errors.New("too many pending executions to preview; restore history persistence first")
			}
		}
		if previous, exists := result[event.EventKey]; !exists || record.Kind == historyRecoveryCompletion || !previous.HasExitCode {
			result[event.EventKey] = event
		}
	}
}

func openHistoryPreviewFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		var stat unix.Stat_t
		err = unix.Fstat(fd, &stat)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Getuid()) {
			err = errors.New("history preview requires an owner-only regular file")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

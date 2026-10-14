package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const historyArchiveFormat = "vuja-history"
const historyArchiveVersion = 1

type historyArchiveDocument struct {
	Format  string                 `json:"format"`
	Version int                    `json:"version"`
	Events  []historyArchiveRecord `json:"events"`
}

// Imported describes the original evidence, not ownership by a replaceable
// adapter. Its presence is required so malformed input cannot promote records.
type historyArchiveRecord struct {
	ID                string    `json:"id"`
	Command           string    `json:"command"`
	NormalizedCommand string    `json:"normalized_command"`
	Cwd               string    `json:"cwd"`
	SubmittedAt       time.Time `json:"submitted_at"`
	StartedAt         time.Time `json:"started_at"`
	CompletedAt       time.Time `json:"completed_at"`
	DurationNS        int64     `json:"duration_ns"`
	ExitCode          *int      `json:"exit_code"`
	Source            string    `json:"source"`
	Host              string    `json:"host"`
	SessionID         string    `json:"session_id"`
	Shell             string    `json:"shell"`
	State             string    `json:"state"`
	HistoryOrder      int       `json:"history_order"`
	Occurrences       int       `json:"occurrences"`
	Imported          *bool     `json:"imported"`
	Aggregate         *bool     `json:"aggregate"`
}

func encodeHistoryArchive(events []scoring.HistoryEvent) ([]byte, error) {
	events, err := scoring.PrepareHistoryArchiveEvents(events)
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].EventKey < events[j].EventKey })
	document := historyArchiveDocument{Format: historyArchiveFormat, Version: historyArchiveVersion,
		Events: make([]historyArchiveRecord, 0, len(events))}
	for _, event := range events {
		imported := event.Imported
		aggregate := event.Aggregate
		record := historyArchiveRecord{ID: event.EventKey, Command: event.Command, NormalizedCommand: event.NormalizedCommand,
			Cwd: event.Cwd, SubmittedAt: event.SubmittedAt, StartedAt: event.StartedAt, CompletedAt: event.CompletedAt,
			DurationNS: int64(event.Duration), Source: event.Source, Host: event.Host, SessionID: event.SessionID,
			Shell: event.Shell, State: event.State, HistoryOrder: event.HistoryOrder, Occurrences: event.Occurrences, Imported: &imported, Aggregate: &aggregate}
		if event.HasExitCode {
			exitCode := event.ExitCode
			record.ExitCode = &exitCode
		}
		document.Events = append(document.Events, record)
	}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("history archive contains unrepresentable metadata")
	}
	if len(data)+1 > scoring.MaxHistoryArchiveBytes {
		return nil, errors.New("history archive exceeds the size limit; no file written")
	}
	return append(data, '\n'), nil
}

func decodeHistoryArchive(reader io.Reader) ([]scoring.HistoryEvent, error) {
	data, err := io.ReadAll(io.LimitReader(reader, scoring.MaxHistoryArchiveBytes+1))
	if err != nil {
		return nil, errors.New("could not read history archive")
	}
	if len(data) > scoring.MaxHistoryArchiveBytes {
		return nil, errors.New("history archive exceeds the size limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("history archive contains invalid text")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document historyArchiveDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invalid history archive format")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("history archive must contain exactly one document")
	}
	if document.Format != historyArchiveFormat {
		return nil, errors.New("unsupported history archive format")
	}
	if document.Version != historyArchiveVersion {
		return nil, fmt.Errorf("unsupported history archive version %d", document.Version)
	}
	if document.Events == nil || len(document.Events) > scoring.MaxHistoryArchiveEvents {
		return nil, errors.New("history archive requires a bounded execution list")
	}
	events := make([]scoring.HistoryEvent, 0, len(document.Events))
	for _, record := range document.Events {
		if record.Imported == nil || record.Aggregate == nil {
			return nil, errors.New("history archive is missing execution provenance or scope")
		}
		event := scoring.HistoryEvent{EventKey: record.ID, Command: record.Command, NormalizedCommand: record.NormalizedCommand,
			Cwd: record.Cwd, SubmittedAt: record.SubmittedAt, StartedAt: record.StartedAt, CompletedAt: record.CompletedAt,
			Duration: time.Duration(record.DurationNS), Source: record.Source, Host: record.Host, SessionID: record.SessionID,
			Shell: record.Shell, State: record.State, HistoryOrder: record.HistoryOrder, Occurrences: record.Occurrences, Imported: *record.Imported, Aggregate: *record.Aggregate}
		if record.ExitCode != nil {
			event.ExitCode, event.HasExitCode = *record.ExitCode, true
		}
		events = append(events, event)
	}
	return scoring.PrepareHistoryArchiveEvents(events)
}

func readHistoryArchiveFile(path string) ([]scoring.HistoryEvent, error) {
	// Nonblocking open and no-follow prevent a replaced path from making a
	// regular-file check block on a FIFO or read through a symlink.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("history archive input must be a readable regular file, not a symlink")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("history archive input must be a regular file")
	}
	if info.Size() > scoring.MaxHistoryArchiveBytes {
		return nil, errors.New("history archive exceeds the size limit")
	}
	return decodeHistoryArchive(file)
}

func writeNewHistoryArchiveFile(path string, data []byte) error {
	// Never chmod an arbitrary destination directory or overwrite an existing
	// backup. Publication links a complete, synced owner-only temporary file.
	if _, err := os.Lstat(path); err == nil {
		return errors.New("history archive destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".vuja-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(config.PrivateFileMode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("history archive destination already exists")
		}
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("archive file published, but directory sync unavailable: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("archive file published, but directory sync failed: %w", err)
	}
	return nil
}

func newHistoryExportCommand() *cobra.Command {
	return &cobra.Command{
		Use: "export <path>", Args: cobra.ExactArgs(1),
		Short: "export canonical history to a private versioned Vuja archive",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, cancel, err := historyCommandStore()
			defer cancel()
			if err != nil {
				return err
			}
			ctx, cancelQuery := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancelQuery()
			events, err := store.QueryHistoryEvents(ctx, scoring.MaxHistoryArchiveEvents+1)
			if err != nil {
				return err
			}
			data, err := encodeHistoryArchive(events)
			if err != nil {
				return err
			}
			if err := writeNewHistoryArchiveFile(args[0], data); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "exported %d canonical execution records; output checkpoints and external history files are separate\n", len(events))
			return nil
		},
	}
}

func newNativeHistoryImportCommand() *cobra.Command {
	var oldSessionsRestarted bool
	command := &cobra.Command{
		Use: "native <path>", Args: cobra.ExactArgs(1),
		Short: "merge a versioned Vuja history archive without external integrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !oldSessionsRestarted {
				return errors.New("restart all pre-update Vuja sessions, then acknowledge with --old-sessions-restarted; legacy adapters cannot preserve archive ownership")
			}
			// Validate the entire input before opening/recovering canonical data.
			events, err := readHistoryArchiveFile(args[0])
			if err != nil {
				return err
			}
			store, _, cancel, err := historyCommandStore()
			defer cancel()
			if err != nil {
				return err
			}
			ctx, cancelMerge := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancelMerge()
			canonicalHistoryMutationMu.Lock()
			defer canonicalHistoryMutationMu.Unlock()
			added, err := store.ImportHistoryArchiveEvents(ctx, events)
			if err != nil {
				return err
			}
			removed, retentionErr := applyConfiguredHistoryRetention(ctx, store, config.Get().History, time.Now())
			_, publicationErr := publishCanonicalStoreHistoryLocked(ctx, store)
			fmt.Fprintf(cmd.OutOrStdout(), "imported %d execution records; %d already present or excluded; retention removed %d; processes are not resumed\n", added, len(events)-added, removed)
			fmt.Fprintln(cmd.OutOrStdout(), "output checkpoints, external history files and saved archives are unchanged")
			if retentionErr != nil || publicationErr != nil {
				return fmt.Errorf("native archive merge committed, but retention or recall refresh failed: %w", errors.Join(retentionErr, publicationErr))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&oldSessionsRestarted, "old-sessions-restarted", false, "acknowledge all pre-update Vuja sessions were restarted before restoring durable archive ownership")
	return command
}

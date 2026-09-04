package integration

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/policy"

	_ "modernc.org/sqlite"
)

type historyOccurrence struct {
	ID          string
	Command     string
	Cwd         string
	Timestamp   time.Time
	ExitCode    int
	HasExitCode bool
	Duration    time.Duration
	Source      string
	Host        string
	SessionID   string
}

const (
	externalHistoryImportLimit   = 100_000
	externalHistoryRecordLimit   = 1024 * 1024
	externalHistoryMetadataLimit = 64 * 1024
	externalHistoryMemoryLimit   = 64 * 1024 * 1024
)

type boundedHistoryBuffer struct {
	slots    []historyOccurrence
	start    int
	size     int
	bytes    int
	maxBytes int
}

func newBoundedHistoryBuffer(maxEntries, maxBytes int) *boundedHistoryBuffer {
	if maxEntries < 0 {
		maxEntries = 0
	}
	return &boundedHistoryBuffer{slots: make([]historyOccurrence, maxEntries), maxBytes: max(maxBytes, 0)}
}

func historyOccurrenceBytes(occurrence historyOccurrence) int {
	return len(occurrence.ID) + len(occurrence.Command) + len(occurrence.Cwd) + len(occurrence.Source) +
		len(occurrence.Host) + len(occurrence.SessionID) + 128
}

func (buffer *boundedHistoryBuffer) Add(occurrence historyOccurrence) bool {
	if buffer == nil || len(buffer.slots) == 0 {
		return false
	}
	size := historyOccurrenceBytes(occurrence)
	if size > buffer.maxBytes {
		return false
	}
	for buffer.size == len(buffer.slots) || buffer.bytes+size > buffer.maxBytes {
		removed := &buffer.slots[buffer.start]
		buffer.bytes -= historyOccurrenceBytes(*removed)
		*removed = historyOccurrence{}
		buffer.start = (buffer.start + 1) % len(buffer.slots)
		buffer.size--
	}
	index := (buffer.start + buffer.size) % len(buffer.slots)
	buffer.slots[index] = occurrence
	buffer.size++
	buffer.bytes += size
	return true
}

func (buffer *boundedHistoryBuffer) Entries() []historyOccurrence {
	if buffer == nil || buffer.size == 0 {
		return nil
	}
	entries := make([]historyOccurrence, 0, buffer.size)
	for index := range buffer.size {
		entries = append(entries, buffer.slots[(buffer.start+index)%len(buffer.slots)])
	}
	return entries
}

func loadShellHistory(file *os.File, shellName string) ([]historyOccurrence, error) {
	return loadShellHistoryContext(context.Background(), file, shellName)
}

func loadShellHistoryContext(ctx context.Context, file *os.File, shellName string) ([]historyOccurrence, error) {
	return loadShellHistoryContextLimit(ctx, file, shellName, externalHistoryImportLimit)
}

func loadShellHistoryContextLimit(ctx context.Context, file *os.File, shellName string, limit int) ([]historyOccurrence, error) {
	if limit <= 0 {
		return nil, nil
	}
	occurrences := newBoundedHistoryBuffer(limit, externalHistoryMemoryLimit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), externalHistoryRecordLimit)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		var occurrence historyOccurrence
		switch shellName {
		case "zsh":
			occurrence = parseZshHistoryLine(line)
		case "fish":
			command, ok := strings.CutPrefix(line, "- cmd: ")
			if !ok {
				continue
			}
			occurrence = historyOccurrence{Command: command, Source: "fish"}
		default:
			if isBashTimestampLine(line) {
				continue
			}
			occurrence = historyOccurrence{Command: line, Source: "bash"}
		}
		var recordable bool
		occurrence.Command, recordable = normalizeInteractiveHistoryCommand(occurrence.Command)
		if recordable {
			occurrences.Add(occurrence)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return occurrences.Entries(), nil
}

func parseZshHistoryLine(line string) historyOccurrence {
	occurrence := historyOccurrence{Command: line, Source: "zsh"}
	header, command, ok := strings.Cut(line, ";")
	if !ok {
		return occurrence
	}
	occurrence.Command = command
	header = strings.TrimSpace(strings.TrimPrefix(header, ":"))
	parts := strings.SplitN(header, ":", 2)
	if len(parts) > 0 {
		if seconds, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64); err == nil {
			occurrence.Timestamp = time.Unix(seconds, 0)
		}
	}
	if len(parts) == 2 {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil {
			occurrence.Duration = time.Duration(seconds * float64(time.Second))
		}
	}
	return occurrence
}

func isBashTimestampLine(line string) bool {
	if !strings.HasPrefix(line, "#") || len(line) == 1 {
		return false
	}
	_, err := strconv.ParseInt(line[1:], 10, 64)
	return err == nil
}

func atuinHistoryPath(home string) string {
	if path := strings.TrimSpace(os.Getenv("ATUIN_DB_PATH")); path != "" {
		return path
	}
	return filepath.Join(home, ".local", "share", "atuin", "history.db")
}

func maxFileModTime(paths ...string) int64 {
	var latest int64
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.ModTime().UnixNano() > latest {
			latest = info.ModTime().UnixNano()
		}
	}
	return latest
}

func loadAtuinHistory(path string) ([]historyOccurrence, error) {
	return loadAtuinHistoryContext(context.Background(), path)
}

func loadAtuinHistoryContext(ctx context.Context, path string) ([]historyOccurrence, error) {
	return loadAtuinHistoryContextLimit(ctx, path, externalHistoryImportLimit)
}

func loadAtuinHistoryContextLimit(ctx context.Context, path string, limit int) ([]historyOccurrence, error) {
	if limit <= 0 {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	columns, err := tableColumnsContext(ctx, db, "history")
	if err != nil {
		return nil, err
	}
	if !columns["command"] {
		return nil, fmt.Errorf("atuin history table has no command column")
	}

	columnExpr := func(name, fallback string) string {
		if columns[name] {
			return "COALESCE(CAST(" + name + " AS TEXT), '')"
		}
		return fallback
	}
	query := "SELECT command, " +
		columnExpr("timestamp", "''") + ", " +
		columnExpr("cwd", "''") + ", " +
		columnExpr("exit", "''") + ", " +
		columnExpr("duration", "''") + ", " +
		columnExpr("id", "''") + ", " +
		columnExpr("hostname", "''") + ", " +
		columnExpr("session", "''") +
		" FROM history WHERE command IS NOT NULL AND length(CAST(command AS BLOB)) <= ?"
	queryArguments := []any{externalHistoryRecordLimit}
	for _, name := range []string{"timestamp", "cwd", "exit", "duration", "id", "hostname", "session"} {
		if columns[name] {
			query += " AND length(CAST(" + name + " AS BLOB)) <= ?"
			queryArguments = append(queryArguments, externalHistoryMetadataLimit)
		}
	}
	if columns["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	order := make([]string, 0, 2)
	if columns["timestamp"] {
		order = append(order, "timestamp DESC")
	}
	if columns["id"] {
		order = append(order, "id DESC")
	} else {
		order = append(order, "rowid DESC")
	}
	query += " ORDER BY " + strings.Join(order, ", ") + " LIMIT ?"
	queryArguments = append(queryArguments, limit)

	rows, err := db.QueryContext(ctx, query, queryArguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	occurrences := make([]historyOccurrence, 0, min(limit, 1024))
	usedBytes := 0
	for rows.Next() {
		var command, timestamp, cwd, exitCode, duration, id, host, sessionID string
		if err := rows.Scan(&command, &timestamp, &cwd, &exitCode, &duration, &id, &host, &sessionID); err != nil {
			continue
		}
		normalizedCommand, recordable := normalizeInteractiveHistoryCommand(command)
		if !recordable {
			continue
		}
		occurrence := historyOccurrence{
			ID:        strings.TrimSpace(id),
			Command:   normalizedCommand,
			Cwd:       strings.TrimSpace(cwd),
			Timestamp: parseAtuinTimestamp(timestamp),
			Source:    "atuin",
			Host:      strings.TrimSpace(host),
			SessionID: strings.TrimSpace(sessionID),
		}
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(exitCode)); parseErr == nil {
			occurrence.ExitCode = parsed
			occurrence.HasExitCode = true
		}
		if nanoseconds, parseErr := strconv.ParseInt(strings.TrimSpace(duration), 10, 64); parseErr == nil {
			occurrence.Duration = time.Duration(nanoseconds)
		}
		size := historyOccurrenceBytes(occurrence)
		if usedBytes+size > externalHistoryMemoryLimit {
			break
		}
		usedBytes += size
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(occurrences)-1; left < right; left, right = left+1, right-1 {
		occurrences[left], occurrences[right] = occurrences[right], occurrences[left]
	}
	return occurrences, nil
}

func DefaultAtuinHistoryPath(home string) string {
	return atuinHistoryPath(home)
}

func LoadAtuinHistoryEntries(ctx context.Context, path string) ([]HistoryEntry, error) {
	occurrences, err := loadAtuinHistoryContext(ctx, path)
	if err != nil {
		return nil, err
	}
	return historyOccurrencesToEntries(occurrences), nil
}

func LoadShellHistoryEntries(ctx context.Context, path, shellName string) ([]HistoryEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	occurrences, err := loadShellHistoryContext(ctx, file, shellName)
	if err != nil {
		return nil, err
	}
	return historyOccurrencesToEntries(occurrences), nil
}

func DefaultShellHistoryPath(home, shellName string) string {
	switch shellName {
	case "zsh":
		return filepath.Join(home, ".zsh_history")
	case "fish":
		return filepath.Join(home, ".local", "share", "fish", "fish_history")
	default:
		return filepath.Join(home, ".bash_history")
	}
}

func normalizeInteractiveHistoryCommand(command string) (string, bool) {
	return policy.HistoryCommand(command, false)
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	return tableColumnsContext(context.Background(), db, table)
}

func tableColumnsContext(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func parseAtuinTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	switch {
	case number > 1_000_000_000_000_000_000:
		return time.Unix(0, number)
	case number > 1_000_000_000_000_000:
		return time.Unix(0, number*int64(time.Microsecond))
	case number > 1_000_000_000_000:
		return time.UnixMilli(number)
	default:
		return time.Unix(number, 0)
	}
}

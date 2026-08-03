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
	"unicode"
	"unicode/utf8"

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

func loadShellHistory(file *os.File, shellName string) ([]historyOccurrence, error) {
	var occurrences []historyOccurrence
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
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
			occurrences = append(occurrences, occurrence)
		}
	}
	return occurrences, scanner.Err()
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
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	columns, err := tableColumns(db, "history")
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
		" FROM history WHERE command IS NOT NULL"
	if columns["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	if columns["timestamp"] {
		query += " ORDER BY timestamp ASC"
	}

	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var occurrences []historyOccurrence
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
		occurrences = append(occurrences, occurrence)
	}
	return occurrences, rows.Err()
}

func normalizeInteractiveHistoryCommand(command string) (string, bool) {
	if command == "" {
		return "", false
	}
	first, _ := utf8.DecodeRuneInString(command)
	if unicode.IsSpace(first) {
		return "", false
	}
	command = strings.TrimSpace(command)
	return command, command != "" && strings.IndexFunc(command, unicode.IsControl) == -1
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
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

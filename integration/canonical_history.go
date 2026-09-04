package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/policy"
	"github.com/versenilvis/fuzzy"
)

const (
	HistoryStateSubmitted   = "submitted"
	HistoryStateRunning     = "running"
	HistoryStateCompleted   = "completed"
	HistoryStateFailed      = "failed"
	HistoryStateInterrupted = "interrupted"
	HistoryStateUnknown     = "unknown"
)

type CanonicalHistoryCounts struct {
	Events     int
	Candidates int
	Eligible   int
}

// ExplainCanonicalHistory reports sanitized counts from the same immutable
// generation used by interactive recall. It intentionally returns no commands.
func ExplainCanonicalHistory(query string) CanonicalHistoryCounts {
	mu.RLock()
	defer mu.RUnlock()
	counts := CanonicalHistoryCounts{Events: historyEventCount, Candidates: len(historyCache)}
	queries := historyQueryAlternatives(query, nil)
	for _, command := range historyCache {
		if historyCommandEligibleForAnyQuery(command, queries, true) {
			counts.Eligible++
		}
	}
	if counts.Eligible == 0 {
		for _, command := range historyCache {
			if historyCommandEligibleForAnyQuery(command, queries, false) {
				counts.Eligible++
			}
		}
	}
	return counts
}

// PublishCanonicalHistory atomically publishes the Vuja-owned event generation
// used by inline suggestions, rich search, and empty-prompt history.
func PublishCanonicalHistory(entries []HistoryEntry) {
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if canonicalHistoryEntryRecordable(entry) {
			entry.searchRunes = nil
			filtered = append(filtered, entry)
		}
	}
	entries = filtered
	prepareHistoryEntries(entries)
	sortHistoryEntries(entries)
	commands, stats, ids := buildCanonicalSearchCache(entries)
	commandIndex := buildHistoryCommandIndex(commands)
	statIndex := buildHistoryStatIndex(stats)
	entryIndex := make(map[string]int, len(entries))
	for index := range entries {
		entryIndex[entries[index].ID] = index
	}
	mu.Lock()
	richHistoryEntries = entries
	historyCache = commands
	historyCommandIndex = commandIndex
	historyStatsCache = stats
	historyStatIndex = statIndex
	historyEventCount = canonicalHistoryOccurrenceCount(entries)
	historyEntryIndex = entryIndex
	idMapCache = ids
	searcherCache = fuzzy.NewPlainSearcher(commands)
	resetIncrementalHistorySearchLocked()
	mu.Unlock()
}

// PublishCanonicalHistoryEntry makes a submitted or updated execution visible
// to every history surface without consulting an external history provider.
func PublishCanonicalHistoryEntry(entry HistoryEntry) {
	publishCanonicalHistoryEntry(entry)
}

func publishCanonicalHistoryEntry(entry HistoryEntry) bool {
	if !canonicalHistoryEntryRecordable(entry) {
		return false
	}
	prepareHistoryEntry(&entry)
	command := strings.TrimSpace(entry.NormalizedCommand)
	if command == "" {
		command = strings.TrimSpace(entry.Command)
	}
	if command == "" {
		return false
	}
	mu.Lock()
	index, existed := historyEntryIndex[entry.ID]
	oldCommand := ""
	if existed {
		oldCommand = strings.TrimSpace(richHistoryEntries[index].NormalizedCommand)
		if oldCommand == "" {
			oldCommand = strings.TrimSpace(richHistoryEntries[index].Command)
		}
		richHistoryEntries[index] = entry
	}
	if !existed {
		historyEntryIndex[entry.ID] = len(richHistoryEntries)
		richHistoryEntries = append(richHistoryEntries, entry)
	}
	if existed && oldCommand != command {
		commands, stats, ids := buildCanonicalSearchCache(richHistoryEntries)
		historyCache = commands
		historyCommandIndex = buildHistoryCommandIndex(commands)
		historyStatsCache = stats
		historyStatIndex = buildHistoryStatIndex(stats)
		idMapCache = ids
		searcherCache = fuzzy.NewPlainSearcher(commands)
		resetIncrementalHistorySearchLocked()
		mu.Unlock()
		return true
	}
	commands := historyCache
	commandIndex, hasCommand := historyCommandIndex[command]
	if !hasCommand {
		commandIndex = -1
	}
	candidateOrderChanged := commandIndex < 0 || (!existed && commandIndex > 0)
	if candidateOrderChanged {
		commands = append([]string(nil), historyCache...)
		if commandIndex >= 0 {
			commands = append(commands[:commandIndex], commands[commandIndex+1:]...)
		}
		commands = append([]string{command}, commands...)
	}
	ids := idMapCache
	if candidateOrderChanged {
		ids = make(map[string]int, len(commands))
		for index, candidate := range commands {
			ids[candidate] = len(commands) - index
		}
		historyCommandIndex = buildHistoryCommandIndex(commands)
	}
	stats := historyStatsCache
	statKey := historyStatKey(command, entry.Cwd)
	statIndex, hasStat := historyStatIndex[statKey]
	if !hasStat {
		stats = append(stats, HistoryStat{
			Command: command,
			Cwd:     strings.TrimSpace(entry.Cwd),
			Count:   max(entry.Occurrences, 1),
		})
		statIndex = len(stats) - 1
		historyStatIndex[statKey] = statIndex
	} else if !existed {
		stats[statIndex].Count += max(entry.Occurrences, 1)
	}
	if !entry.StartedAt.Before(stats[statIndex].LastUsed) {
		stats[statIndex].LastUsed = entry.StartedAt
		stats[statIndex].ExitCode = entry.ExitCode
		stats[statIndex].HasExitCode = entry.HasExitCode
		stats[statIndex].Duration = entry.Duration
		stats[statIndex].Source = entry.Source
	}
	historyCache = commands
	historyStatsCache = stats
	if !existed {
		historyEventCount += max(entry.Occurrences, 1)
	}
	idMapCache = ids
	if candidateOrderChanged {
		searcherCache = fuzzy.NewPlainSearcher(commands)
		resetIncrementalHistorySearchLocked()
	}
	mu.Unlock()
	return existed
}

func canonicalHistoryEntryRecordable(entry HistoryEntry) bool {
	exact, recordable := normalizeInteractiveHistoryCommand(entry.Command)
	if !recordable || policy.IsSensitive(exact) {
		return false
	}
	normalized := strings.TrimSpace(entry.NormalizedCommand)
	if normalized == "" {
		normalized = exact
	}
	return normalized != "" && !policy.IsSensitive(normalized)
}

func buildHistoryCommandIndex(commands []string) map[string]int {
	index := make(map[string]int, len(commands))
	for position, command := range commands {
		index[command] = position
	}
	return index
}

func buildHistoryStatIndex(stats []HistoryStat) map[string]int {
	index := make(map[string]int, len(stats))
	for position := range stats {
		index[historyStatKey(stats[position].Command, stats[position].Cwd)] = position
	}
	return index
}

func historyStatKey(command, cwd string) string {
	return strings.TrimSpace(command) + "\x00" + strings.TrimSpace(cwd)
}

func buildCanonicalSearchCache(entries []HistoryEntry) ([]string, []HistoryStat, map[string]int) {
	seen := make(map[string]bool, len(entries))
	commands := make([]string, 0, len(entries))
	ids := make(map[string]int, len(entries))
	byCommandAndDirectory := make(map[string]*HistoryStat)
	for _, entry := range entries {
		command := strings.TrimSpace(entry.NormalizedCommand)
		if command == "" {
			command = strings.TrimSpace(entry.Command)
		}
		if command == "" {
			continue
		}
		if !seen[command] {
			seen[command] = true
			commands = append(commands, command)
			ids[command] = len(entries) - len(commands) + 1
		}

		key := command + "\x00" + strings.TrimSpace(entry.Cwd)
		stat := byCommandAndDirectory[key]
		if stat == nil {
			stat = &HistoryStat{Command: command, Cwd: strings.TrimSpace(entry.Cwd), Source: entry.Source}
			byCommandAndDirectory[key] = stat
		}
		stat.Count += max(entry.Occurrences, 1)
		if entry.StartedAt.After(stat.LastUsed) || stat.LastUsed.IsZero() {
			stat.LastUsed = entry.StartedAt
			stat.ExitCode = entry.ExitCode
			stat.HasExitCode = entry.HasExitCode
			stat.Duration = entry.Duration
			stat.Source = entry.Source
		}
	}

	stats := make([]HistoryStat, 0, len(byCommandAndDirectory))
	for _, stat := range byCommandAndDirectory {
		stats = append(stats, *stat)
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if !stats[i].LastUsed.Equal(stats[j].LastUsed) {
			return stats[i].LastUsed.After(stats[j].LastUsed)
		}
		if stats[i].Command != stats[j].Command {
			return stats[i].Command < stats[j].Command
		}
		return stats[i].Cwd < stats[j].Cwd
	})

	return commands, stats, ids
}

func canonicalHistoryOccurrenceCount(entries []HistoryEntry) int {
	count := 0
	for _, entry := range entries {
		count += max(entry.Occurrences, 1)
	}
	return count
}

func commandHistoryIdentity(command, cwd string, startedAt time.Time) string {
	sum := sha256.Sum256([]byte(command + "\x00" + cwd + "\x00" + startedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:12])
}

package integration

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	richHistoryMu          sync.RWMutex
	richHistoryEntries     []HistoryEntry
	liveHistoryEntries     []HistoryEntry
	persistentHistoryCache []HistoryEntry
)

type MatchRange struct {
	Start int
	End   int
}

type HistoryEntry struct {
	ID          string
	Command     string
	Cwd         string
	StartedAt   time.Time
	Duration    time.Duration
	ExitCode    int
	HasExitCode bool
	Source      string
	Host        string
	SessionID   string
	searchRunes []rune
}

type RichHistoryResult struct {
	HistoryEntry
	RelativeTime string
	MatchRanges  []MatchRange
	Score        int
}

type RichHistorySearchOptions struct {
	Now            time.Time
	Limit          int
	Cwd            string
	ProjectRoot    string
	Scope          HistoryScope
	SuccessfulOnly bool
	Host           string
	SessionID      string
}

func historyOccurrencesToEntries(occurrences []historyOccurrence) []HistoryEntry {
	entries := make([]HistoryEntry, 0, len(occurrences))
	for index, occurrence := range occurrences {
		id := strings.TrimSpace(occurrence.ID)
		if id == "" {
			sum := sha256.Sum256([]byte(fmt.Sprintf(
				"%s\x00%d\x00%s\x00%s\x00%d\x00%d",
				occurrence.Source,
				index,
				occurrence.Command,
				occurrence.Cwd,
				occurrence.Timestamp.UnixNano(),
				occurrence.Duration.Nanoseconds(),
			)))
			id = fmt.Sprintf("%x", sum[:12])
		}
		source := strings.TrimSpace(occurrence.Source)
		if source == "" {
			source = "shell"
		}
		entries = append(entries, HistoryEntry{
			ID:          source + ":" + id,
			Command:     occurrence.Command,
			Cwd:         occurrence.Cwd,
			StartedAt:   occurrence.Timestamp,
			Duration:    occurrence.Duration,
			ExitCode:    occurrence.ExitCode,
			HasExitCode: occurrence.HasExitCode,
			Source:      source,
			Host:        occurrence.Host,
			SessionID:   occurrence.SessionID,
			searchRunes: lowerRunes(occurrence.Command),
		})
	}
	return entries
}

func setPersistentHistoryEntries(entries []HistoryEntry) {
	richHistoryMu.Lock()
	defer richHistoryMu.Unlock()
	prepareHistoryEntries(entries)
	sortHistoryEntries(entries)
	persistentHistoryCache = append([]HistoryEntry(nil), entries...)
	if len(richHistoryEntries) == 0 {
		richHistoryEntries = append([]HistoryEntry(nil), entries...)
	}
}

func PersistentHistoryEntriesSnapshot() []HistoryEntry {
	richHistoryMu.RLock()
	defer richHistoryMu.RUnlock()
	return append([]HistoryEntry(nil), persistentHistoryCache...)
}

func ReplaceRichHistoryEntries(entries []HistoryEntry) {
	richHistoryMu.Lock()
	defer richHistoryMu.Unlock()
	prepareHistoryEntries(entries)
	seen := make(map[string]bool, len(entries)+len(liveHistoryEntries))
	for _, entry := range entries {
		seen[entry.ID] = true
	}
	for _, entry := range liveHistoryEntries {
		if !seen[entry.ID] {
			entries = append(entries, entry)
			seen[entry.ID] = true
		}
	}
	sortHistoryEntries(entries)
	richHistoryEntries = append([]HistoryEntry(nil), entries...)
}

func AppendRichHistoryEntry(entry HistoryEntry) {
	richHistoryMu.Lock()
	defer richHistoryMu.Unlock()
	prepareHistoryEntry(&entry)
	liveHistoryEntries = append([]HistoryEntry{entry}, liveHistoryEntries...)
	richHistoryEntries = append([]HistoryEntry{entry}, richHistoryEntries...)
}

func RichHistorySnapshot() []HistoryEntry {
	richHistoryMu.RLock()
	defer richHistoryMu.RUnlock()
	return append([]HistoryEntry(nil), richHistoryEntries...)
}

func SearchRichHistory(entries []HistoryEntry, query string, options RichHistorySearchOptions) []RichHistoryResult {
	return searchRichHistory(entries, query, options, false)
}

func SearchCurrentRichHistory(query string, options RichHistorySearchOptions) []RichHistoryResult {
	richHistoryMu.RLock()
	entries := richHistoryEntries
	richHistoryMu.RUnlock()
	// Published history generations are immutable. Search the pinned generation
	// off-lock so a large Ctrl+R query cannot block recording a completed command.
	return searchRichHistory(entries, query, options, true)
}

func searchRichHistory(entries []HistoryEntry, query string, options RichHistorySearchOptions, newestFirst bool) []RichHistoryResult {
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	results := make([]RichHistoryResult, 0, min(len(entries), limit*2))
	queryRunes := lowerRunes(strings.TrimSpace(query))
	for _, entry := range entries {
		if !richHistoryEntryMatchesScope(entry, options) {
			continue
		}
		if options.SuccessfulOnly && (!entry.HasExitCode || entry.ExitCode != 0) {
			continue
		}
		if newestFirst && len(queryRunes) == 0 {
			results = append(results, RichHistoryResult{
				HistoryEntry: entry,
				RelativeTime: formatRelativeTime(entry.StartedAt, now),
			})
			if len(results) == limit {
				return results
			}
			continue
		}
		commandRunes := entry.searchRunes
		if len(commandRunes) == 0 && entry.Command != "" {
			commandRunes = lowerRunes(entry.Command)
		}
		score, ranges, ok := matchHistoryCommandRunes(commandRunes, queryRunes)
		if !ok {
			continue
		}
		results = append(results, RichHistoryResult{
			HistoryEntry: entry,
			RelativeTime: formatRelativeTime(entry.StartedAt, now),
			MatchRanges:  ranges,
			Score:        score,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].StartedAt.After(results[j].StartedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func richHistoryEntryMatchesScope(entry HistoryEntry, options RichHistorySearchOptions) bool {
	switch options.Scope {
	case HistoryScopeDirectory:
		return filepath.Clean(entry.Cwd) == filepath.Clean(options.Cwd)
	case HistoryScopeProject:
		if strings.TrimSpace(options.ProjectRoot) == "" || strings.TrimSpace(entry.Cwd) == "" {
			return false
		}
		relative, err := filepath.Rel(options.ProjectRoot, entry.Cwd)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	case HistoryScopeMachine:
		return strings.TrimSpace(options.Host) != "" && entry.Host == options.Host
	case HistoryScopeSession:
		return strings.TrimSpace(options.SessionID) != "" && entry.SessionID == options.SessionID
	default:
		return true
	}
}

func matchHistoryCommandRunes(commandRunes, queryRunes []rune) (int, []MatchRange, bool) {
	if len(queryRunes) == 0 {
		return 0, nil, true
	}

	ranges := literalMatchRanges(commandRunes, queryRunes)
	if len(ranges) > 0 {
		first := ranges[0].Start
		return 1_000_000 - first*100 - len(commandRunes), ranges, true
	}

	if !isFuzzyMatch(commandRunes, queryRunes) {
		return 0, nil, false
	}
	positions, ok := fuzzyMatchPositions(commandRunes, queryRunes)
	if !ok {
		return 0, nil, false
	}
	ranges = compressMatchPositions(positions)
	span := positions[len(positions)-1] - positions[0] + 1
	return 100_000 - span*100 - positions[0]*10 - len(commandRunes), ranges, true
}

func prepareHistoryEntries(entries []HistoryEntry) {
	for index := range entries {
		prepareHistoryEntry(&entries[index])
	}
}

func sortHistoryEntries(entries []HistoryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
}

func prepareHistoryEntry(entry *HistoryEntry) {
	if entry != nil && len(entry.searchRunes) == 0 && entry.Command != "" {
		entry.searchRunes = lowerRunes(entry.Command)
	}
}

func literalMatchRanges(command, query []rune) []MatchRange {
	if len(query) == 0 || len(query) > len(command) {
		return nil
	}

	var matches []MatchRange
	for start := 0; start <= len(command)-len(query); {
		matched := true
		for index := range query {
			if command[start+index] != query[index] {
				matched = false
				break
			}
		}
		if !matched {
			start++
			continue
		}
		matches = append(matches, MatchRange{Start: start, End: start + len(query)})
		start += len(query)
	}
	return matches
}

func fuzzyMatchPositions(command, query []rune) ([]int, bool) {
	if len(query) == 0 {
		return nil, true
	}
	candidate := make([]int, len(query))
	var best []int
	bestSpan := len(command) + 1
	for start, commandRune := range command {
		if commandRune != query[0] {
			continue
		}
		candidate[0] = start
		commandIndex := start + 1
		matched := true
		for queryIndex := 1; queryIndex < len(query); queryIndex++ {
			for commandIndex < len(command) && command[commandIndex] != query[queryIndex] {
				commandIndex++
			}
			if commandIndex == len(command) {
				matched = false
				break
			}
			candidate[queryIndex] = commandIndex
			commandIndex++
		}
		if !matched {
			continue
		}
		span := candidate[len(candidate)-1] - candidate[0] + 1
		if span < bestSpan {
			bestSpan = span
			best = append(best[:0], candidate...)
		}
	}
	return best, len(best) > 0
}

func isFuzzyMatch(command, query []rune) bool {
	commandIndex := 0
	for _, queryRune := range query {
		for commandIndex < len(command) && command[commandIndex] != queryRune {
			commandIndex++
		}
		if commandIndex == len(command) {
			return false
		}
		commandIndex++
	}
	return true
}

func compressMatchPositions(positions []int) []MatchRange {
	if len(positions) == 0 {
		return nil
	}
	ranges := []MatchRange{{Start: positions[0], End: positions[0] + 1}}
	for _, position := range positions[1:] {
		last := &ranges[len(ranges)-1]
		if position == last.End {
			last.End++
			continue
		}
		ranges = append(ranges, MatchRange{Start: position, End: position + 1})
	}
	return ranges
}

func lowerRunes(value string) []rune {
	runes := []rune(value)
	for index := range runes {
		runes[index] = unicode.ToLower(runes[index])
	}
	return runes
}

func formatRelativeTime(startedAt, now time.Time) string {
	if startedAt.IsZero() {
		return ""
	}
	elapsed := now.Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	default:
		return startedAt.Format("2006-01-02")
	}
}

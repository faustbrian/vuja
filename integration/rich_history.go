package integration

import (
	"container/heap"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

var richHistoryEntries []HistoryEntry

type MatchRange struct {
	Start int
	End   int
}

type HistoryEntry struct {
	ID                string
	Command           string
	NormalizedCommand string
	Cwd               string
	SubmittedAt       time.Time
	StartedAt         time.Time
	CompletedAt       time.Time
	Duration          time.Duration
	ExitCode          int
	HasExitCode       bool
	Source            string
	Host              string
	SessionID         string
	Shell             string
	State             string
	HistoryOrder      int
	Occurrences       int
	searchRunes       []rune
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
		state := HistoryStateUnknown
		if occurrence.HasExitCode {
			state = HistoryStateCompleted
			if occurrence.ExitCode != 0 {
				state = HistoryStateFailed
			}
		}
		completedAt := time.Time{}
		if !occurrence.Timestamp.IsZero() && occurrence.Duration > 0 {
			completedAt = occurrence.Timestamp.Add(occurrence.Duration)
		}
		shellName := ""
		if source == "zsh" || source == "bash" || source == "fish" {
			shellName = source
		}
		entries = append(entries, HistoryEntry{
			ID:                source + ":" + id,
			Command:           occurrence.Command,
			NormalizedCommand: occurrence.Command,
			Cwd:               occurrence.Cwd,
			SubmittedAt:       occurrence.Timestamp,
			StartedAt:         occurrence.Timestamp,
			CompletedAt:       completedAt,
			Duration:          occurrence.Duration,
			ExitCode:          occurrence.ExitCode,
			HasExitCode:       occurrence.HasExitCode,
			Source:            source,
			Host:              occurrence.Host,
			SessionID:         occurrence.SessionID,
			Shell:             shellName,
			State:             state,
			searchRunes:       lowerRunes(occurrence.Command),
			HistoryOrder:      index + 1,
		})
	}
	return entries
}

func PersistentHistoryEntriesSnapshot() []HistoryEntry {
	return RichHistorySnapshot()
}

func ReplaceRichHistoryEntries(entries []HistoryEntry) {
	PublishCanonicalHistory(entries)
}

func AppendRichHistoryEntry(entry HistoryEntry) {
	PublishCanonicalHistoryEntry(entry)
}

func UpsertRichHistoryEntry(entry HistoryEntry) bool {
	return publishCanonicalHistoryEntry(entry)
}

func RichHistorySnapshot() []HistoryEntry {
	mu.RLock()
	entries := append([]HistoryEntry(nil), richHistoryEntries...)
	mu.RUnlock()
	sortHistoryEntries(entries)
	return entries
}

func SearchRichHistory(entries []HistoryEntry, query string, options RichHistorySearchOptions) []RichHistoryResult {
	return searchRichHistory(entries, query, options, false)
}

func SearchCurrentRichHistory(query string, options RichHistorySearchOptions) []RichHistoryResult {
	mu.RLock()
	defer mu.RUnlock()
	// The read lock pins the indexed generation without copying an unbounded
	// event slice on every Ctrl+R keystroke. Publication swaps or updates the
	// generation only after an active search finishes.
	return searchRichHistory(richHistoryEntries, query, options, true)
}

func RecentRichHistory(entries []HistoryEntry, now time.Time, limit int) []RichHistoryResult {
	ordered := append([]HistoryEntry(nil), entries...)
	prepareHistoryEntries(ordered)
	return searchRichHistory(ordered, "", RichHistorySearchOptions{Now: now, Limit: limit}, true)
}

func CurrentRecentRichHistory(now time.Time, limit int) []RichHistoryResult {
	mu.RLock()
	defer mu.RUnlock()
	return recentRichHistory(richHistoryEntries, RichHistorySearchOptions{Now: now, Limit: limit})
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
	if newestFirst && len(strings.TrimSpace(query)) == 0 {
		return recentRichHistory(entries, options)
	}
	results := make(richHistoryResultHeap, 0, min(len(entries), limit))
	queryRunes := lowerRunes(strings.TrimSpace(query))
	for _, entry := range entries {
		if !richHistoryEntryMatchesScope(entry, options) {
			continue
		}
		if options.SuccessfulOnly && (!entry.HasExitCode || entry.ExitCode != 0) {
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
		candidate := RichHistoryResult{
			HistoryEntry: entry,
			RelativeTime: formatRelativeTime(entry.StartedAt, now),
			MatchRanges:  ranges,
			Score:        score,
		}
		if len(results) < limit {
			heap.Push(&results, candidate)
		} else if richHistoryResultBetter(candidate, results[0]) {
			results[0] = candidate
			heap.Fix(&results, 0)
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		return richHistoryResultBetter(results[i], results[j])
	})
	return results
}

type richHistoryResultHeap []RichHistoryResult

func (results richHistoryResultHeap) Len() int { return len(results) }

func (results richHistoryResultHeap) Less(i, j int) bool {
	return richHistoryResultBetter(results[j], results[i])
}

func (results richHistoryResultHeap) Swap(i, j int) { results[i], results[j] = results[j], results[i] }

func (results *richHistoryResultHeap) Push(value any) {
	*results = append(*results, value.(RichHistoryResult))
}

func (results *richHistoryResultHeap) Pop() any {
	old := *results
	last := len(old) - 1
	value := old[last]
	*results = old[:last]
	return value
}

func richHistoryResultBetter(left, right RichHistoryResult) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	return historyEntryNewer(left.HistoryEntry, right.HistoryEntry)
}

func recentRichHistory(entries []HistoryEntry, options RichHistorySearchOptions) []RichHistoryResult {
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	results := make(richHistoryResultHeap, 0, min(len(entries), limit))
	for _, entry := range entries {
		if !richHistoryEntryMatchesScope(entry, options) ||
			options.SuccessfulOnly && (!entry.HasExitCode || entry.ExitCode != 0) {
			continue
		}
		candidate := RichHistoryResult{HistoryEntry: entry, RelativeTime: formatRelativeTime(entry.StartedAt, now)}
		if len(results) < limit {
			heap.Push(&results, candidate)
		} else if richHistoryResultBetter(candidate, results[0]) {
			results[0] = candidate
			heap.Fix(&results, 0)
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return richHistoryResultBetter(results[i], results[j]) })
	return results
}

func historyEntryNewer(left, right HistoryEntry) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	if left.HistoryOrder != right.HistoryOrder {
		return left.HistoryOrder > right.HistoryOrder
	}
	return left.ID > right.ID
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
		if !entries[i].StartedAt.Equal(entries[j].StartedAt) {
			return entries[i].StartedAt.After(entries[j].StartedAt)
		}
		if entries[i].HistoryOrder != entries[j].HistoryOrder {
			return entries[i].HistoryOrder > entries[j].HistoryOrder
		}
		return entries[i].ID > entries[j].ID
	})
}

func prepareHistoryEntry(entry *HistoryEntry) {
	if entry == nil {
		return
	}
	entry.NormalizedCommand = strings.TrimSpace(entry.NormalizedCommand)
	if entry.NormalizedCommand == "" {
		entry.NormalizedCommand = strings.TrimSpace(entry.Command)
	}
	if len(entry.searchRunes) == 0 && entry.NormalizedCommand != "" {
		entry.searchRunes = lowerRunes(entry.NormalizedCommand)
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

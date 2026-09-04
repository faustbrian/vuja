package integration

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/policy"
	"github.com/versenilvis/fuzzy"
)

var (
	historyCache        []string
	historyCommandIndex map[string]int
	historyStatsCache   []HistoryStat
	historyStatIndex    map[string]int
	historyEventCount   int
	historyEntryIndex   map[string]int
	idMapCache          map[string]int
	searcherCache       *fuzzy.Searcher
	mu                  sync.RWMutex
	historySearchMu     sync.Mutex
	lastSearchQuery     string
	lastSearchAliases   string
	lastSearchResults   []HistResult
	lastSearchTruncated bool
)

func RecordSessionCommand(cmd string) {
	RecordSessionCommandAt(cmd, "")
}

func RecordSessionCommandAt(cmd, cwd string) {
	var recordable bool
	cmd, recordable = normalizeInteractiveHistoryCommand(cmd)
	if !recordable || policy.IsSensitive(cmd) {
		return
	}
	recordedAt := time.Now()
	PublishCanonicalHistoryEntry(HistoryEntry{
		ID:          "session:" + commandHistoryIdentity(cmd, cwd, recordedAt),
		Command:     cmd,
		Cwd:         strings.TrimSpace(cwd),
		SubmittedAt: recordedAt,
		StartedAt:   recordedAt,
		Source:      "session",
		State:       HistoryStateCompleted,
	})
}

type HistResult struct {
	ID         int
	Cmd        string
	FuzzyScore int
}

type HistorySearchStatus struct {
	Query      string
	Events     int
	Candidates int
	Matches    int
	Truncated  bool
}

func CurrentHistorySearchStatus() HistorySearchStatus {
	historySearchMu.Lock()
	status := HistorySearchStatus{
		Query:     lastSearchQuery,
		Matches:   len(lastSearchResults),
		Truncated: lastSearchTruncated,
	}
	historySearchMu.Unlock()
	mu.RLock()
	status.Events = historyEventCount
	status.Candidates = len(historyCache)
	mu.RUnlock()
	return status
}

type HistoryStat struct {
	Command     string
	Cwd         string
	Count       int
	LastUsed    time.Time
	ExitCode    int
	HasExitCode bool
	Duration    time.Duration
	Source      string
}

func HistorySnapshot() []HistoryStat {
	mu.RLock()
	defer mu.RUnlock()

	return append([]HistoryStat(nil), historyStatsCache...)
}

func HistoryLoaded() bool {
	mu.RLock()
	defer mu.RUnlock()
	return historyCache != nil
}

func EnsureHistoryLoaded(ctx context.Context) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

// SearchCachedHistory returns only already-loaded history and never waits for a
// concurrent history import or reload. The boolean reports whether a cache was
// available, including an available cache with no matches.
func SearchCachedHistory(query string, aliases map[string]string) ([]HistResult, bool) {
	if !mu.TryRLock() {
		return nil, false
	}
	if historyCache == nil {
		mu.RUnlock()
		return nil, false
	}
	// Published history snapshots are immutable. Pin their slice and map while
	// holding the read lock, then search them without copying on every keystroke.
	commands := historyCache
	ids := idMapCache
	mu.RUnlock()

	queries := historyQueryAlternatives(query, aliases)
	collect := func(requireSubcommand bool) []HistResult {
		results := make([]HistResult, 0, min(len(commands), 1000))
		for _, command := range commands {
			if !historyCommandEligibleForAnyQuery(command, queries, requireSubcommand) {
				continue
			}
			results = append(results, HistResult{ID: ids[command], Cmd: command})
			if len(results) == 1000 {
				break
			}
		}
		return results
	}
	results := collect(true)
	if len(results) == 0 {
		results = collect(false)
	}
	return results, true
}

func historyQueryAlternatives(query string, aliases map[string]string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	queries := []string{query}
	for name, target := range aliases {
		name = strings.ToLower(strings.TrimSpace(name))
		target = strings.ToLower(strings.TrimSpace(target))
		if query == name && target != "" {
			queries = append(queries, target)
		} else if query == target && name != "" {
			queries = append(queries, name)
		} else if name != "" && target != "" && strings.HasPrefix(query, name+" ") {
			queries = append(queries, target+query[len(name):])
		} else if name != "" && target != "" && strings.HasPrefix(query, target+" ") {
			queries = append(queries, name+query[len(target):])
		}
	}
	sort.Strings(queries[1:])
	return queries
}

func historyCommandEligibleForAnyQuery(command string, queries []string, requireSubcommand bool) bool {
	for _, query := range queries {
		if historyCommandEligible(command, query, requireSubcommand) {
			return true
		}
	}
	return false
}

func historyCommandEligible(command, query string, requireSubcommand bool) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	fields := strings.Fields(strings.ToLower(command))
	if len(fields) == 0 {
		return false
	}
	queryFields := strings.Fields(query)
	if len(queryFields) == 0 || !strings.Contains(query, " ") {
		return strings.HasPrefix(fields[0], query)
	}
	if fields[0] != queryFields[0] {
		return false
	}
	if !requireSubcommand {
		return true
	}
	querySecondWord := ""
	for _, field := range queryFields[1:] {
		if !strings.HasPrefix(field, "-") {
			querySecondWord = field
			break
		}
	}
	if querySecondWord == "" {
		return true
	}
	return len(fields) > 1 && strings.HasPrefix(fields[1], querySecondWord)
}

func init() {
	historyEntryIndex = make(map[string]int)
	historyCommandIndex = make(map[string]int)
	historyStatIndex = make(map[string]int)
	idMapCache = make(map[string]int)
	searcherCache = fuzzy.NewPlainSearcher(nil)
}

func ensureHistoryCache(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx.Err()
}

func SearchHistory(query string, aliases map[string]string) ([]HistResult, error) {
	return SearchHistoryContext(context.Background(), query, aliases)
}

func SearchHistoryContext(ctx context.Context, query string, aliases map[string]string) ([]HistResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ensureHistoryCache(ctx); err != nil {
		return nil, err
	}
	mu.RLock()
	// The cache is replaced as one immutable generation and never mutated after
	// publication, so readers can safely retain these references off-lock.
	history := historyCache
	statsIDs := idMapCache
	searcher := searcherCache
	mu.RUnlock()

	if query == "" {
		var results []HistResult
		limit := min(len(history), 100)

		for i := range limit {
			cmd := history[i]
			results = append(results, HistResult{
				ID:  statsIDs[cmd],
				Cmd: cmd,
			})
		}
		return results, nil
	}

	var alternativeQueries []string
	for name, target := range aliases {
		if target != "" {
			qLow := strings.ToLower(query)
			tLow := strings.ToLower(target)
			nLow := strings.ToLower(name)

			if qLow == tLow {
				alternativeQueries = append(alternativeQueries, name)
			} else if strings.HasPrefix(qLow, tLow+" ") {
				suffix := query[len(target):]
				alternativeQueries = append(alternativeQueries, name+suffix)
			}

			if qLow == nLow {
				alternativeQueries = append(alternativeQueries, target)
			} else if strings.HasPrefix(qLow, nLow+" ") {
				suffix := query[len(name):]
				alternativeQueries = append(alternativeQueries, target+suffix)
			}
		}
	}
	sort.Strings(alternativeQueries)

	var results []HistResult
	seenCmds := make(map[string]bool)
	candidateHistory := history
	candidateSearcher := searcher
	aliasesKey := historyAliasesKey(aliases)
	historySearchMu.Lock()
	previousQuery := lastSearchQuery
	previousAliases := lastSearchAliases
	previousResults := append([]HistResult(nil), lastSearchResults...)
	previousTruncated := lastSearchTruncated
	historySearchMu.Unlock()
	if previousQuery != "" && strings.HasPrefix(strings.ToLower(query), strings.ToLower(previousQuery)) &&
		aliasesKey == previousAliases && len(previousResults) > 0 && !previousTruncated {
		candidateHistory = make([]string, 0, len(previousResults))
		for _, result := range previousResults {
			candidateHistory = append(candidateHistory, result.Cmd)
		}
		candidateSearcher = fuzzy.NewPlainSearcher(candidateHistory)
	}

	truncated := false
	addMatches := func(q string, subcmdFilter bool) {
		if ctx.Err() != nil {
			return
		}
		qLow := strings.ToLower(q)
		queryFirstWord := ""
		querySecondWord := ""
		if strings.IndexByte(qLow, ' ') != -1 {
			if fields := strings.Fields(qLow); len(fields) > 0 {
				queryFirstWord = fields[0]
				// find first non-flag token after the command as the subcommand
				for _, f := range fields[1:] {
					if !strings.HasPrefix(f, "-") {
						querySecondWord = f
						break
					}
				}
			}
		}

		// extract pure prefix matches based strictly on recency order (historyCache is newest-first)
		strictMatches := 0
		for _, cmd := range candidateHistory {
			if ctx.Err() != nil {
				return
			}
			if seenCmds[cmd] {
				continue
			}
			fields := strings.Fields(cmd)
			firstWordLow := ""
			if len(fields) > 0 {
				firstWordLow = strings.ToLower(fields[0])
			}

			if queryFirstWord != "" {
				if firstWordLow != queryFirstWord {
					continue
				}
				if subcmdFilter && querySecondWord != "" {
					if len(fields) < 2 {
						continue
					}
					secondWordLow := strings.ToLower(fields[1])
					if !strings.HasPrefix(secondWordLow, querySecondWord) {
						continue
					}
				}
			} else {
				if !strings.HasPrefix(firstWordLow, qLow) {
					continue
				}
			}

			strictMatches++
			if strictMatches > 1000 {
				truncated = true
				continue
			}
			seenCmds[cmd] = true
			results = append(results, HistResult{
				ID:         statsIDs[cmd],
				Cmd:        cmd,
				FuzzyScore: 10000,
			})
		}

		fuzzyHistory := make([]string, 0, len(candidateHistory))
		for _, command := range candidateHistory {
			fields := strings.Fields(command)
			firstWordLow := ""
			if len(fields) > 0 {
				firstWordLow = strings.ToLower(fields[0])
			}
			if queryFirstWord != "" {
				if firstWordLow != queryFirstWord {
					continue
				}
				if subcmdFilter && querySecondWord != "" {
					if len(fields) < 2 || !strings.HasPrefix(strings.ToLower(fields[1]), querySecondWord) {
						continue
					}
				}
			} else if !strings.HasPrefix(firstWordLow, qLow) {
				continue
			}
			fuzzyHistory = append(fuzzyHistory, command)
		}
		fuzzySearcher := candidateSearcher
		if len(fuzzyHistory) != len(candidateHistory) {
			fuzzySearcher = fuzzy.NewPlainSearcher(fuzzyHistory)
		}
		matches := fuzzySearcher.SearchWithScores(q, &fuzzy.SearchOptions{Limit: 1001})
		if len(matches) > 1000 {
			truncated = true
			matches = matches[:1000]
		}
		for _, m := range matches {
			if ctx.Err() != nil {
				return
			}
			if seenCmds[m.Str] {
				continue
			}

			// filter results by command name match
			fields := strings.Fields(m.Str)
			firstWord := m.Str
			if len(fields) > 0 {
				firstWord = fields[0]
			}
			firstWordLow := strings.ToLower(firstWord)

			if queryFirstWord != "" {
				if firstWordLow != queryFirstWord {
					continue
				}
				// when query has a non-flag second token, filter by subcommand prefix
				if subcmdFilter && querySecondWord != "" {
					if len(fields) < 2 {
						continue
					}
					secondWordLow := strings.ToLower(fields[1])
					if !strings.HasPrefix(secondWordLow, querySecondWord) {
						continue
					}
				}
			} else {
				if !strings.HasPrefix(firstWordLow, qLow) {
					continue
				}
			}

			seenCmds[m.Str] = true
			results = append(results, HistResult{
				ID:         statsIDs[m.Str],
				Cmd:        m.Str,
				FuzzyScore: m.Score,
			})
		}
	}

	addMatches(query, true)
	for _, altQ := range alternativeQueries {
		addMatches(altQ, true)
	}

	// fallback: if subcommand filter produced nothing, retry without it
	// so typos like "git chckout" still surface fuzzy matches
	if len(results) == 0 {
		addMatches(query, false)
		for _, altQ := range alternativeQueries {
			addMatches(altQ, false)
		}
	}

	getTier := func(cmd, q string) int {
		bestTier := 4
		check := func(ql string) {
			cmdLow := strings.ToLower(cmd)
			qlLow := strings.ToLower(ql)
			tier := 4
			if cmdLow == qlLow {
				tier = 1
			} else if strings.HasPrefix(cmdLow, qlLow) {
				tier = 2
			} else if strings.Contains(cmdLow, qlLow) {
				tier = 3
			}
			if tier < bestTier {
				bestTier = tier
			}
		}
		check(q)
		for _, altQ := range alternativeQueries {
			check(altQ)
		}
		return bestTier
	}

	tiers := make(map[string]int, len(results))
	for _, result := range results {
		tiers[result.Cmd] = getTier(result.Cmd, query)
	}

	sort.SliceStable(results, func(i, j int) bool {
		tI := tiers[results[i].Cmd]
		tJ := tiers[results[j].Cmd]
		if tI != tJ {
			return tI < tJ
		}

		if tI == 4 && results[i].FuzzyScore != results[j].FuzzyScore {
			return results[i].FuzzyScore > results[j].FuzzyScore
		}

		return results[i].ID > results[j].ID
	})
	if len(results) > 1000 {
		results = results[:1000]
		truncated = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	historySearchMu.Lock()
	lastSearchQuery = query
	lastSearchAliases = aliasesKey
	lastSearchResults = append([]HistResult(nil), results...)
	lastSearchTruncated = truncated
	historySearchMu.Unlock()
	return results, nil
}

func historyAliasesKey(aliases map[string]string) string {
	if len(aliases) == 0 {
		return ""
	}
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var key strings.Builder
	for _, name := range keys {
		key.WriteString(name)
		key.WriteByte('=')
		key.WriteString(aliases[name])
		key.WriteByte('\x00')
	}
	return key.String()
}

func resetIncrementalHistorySearchLocked() {
	historySearchMu.Lock()
	lastSearchQuery = ""
	lastSearchAliases = ""
	lastSearchResults = nil
	lastSearchTruncated = false
	historySearchMu.Unlock()
}

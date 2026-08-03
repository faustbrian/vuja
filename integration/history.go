package integration

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/versenilvis/fuzzy"
)

var (
	sessionHistory   []sessionHistoryEntry
	sessionHistoryMu sync.Mutex

	historyCache      []string
	historyStatsCache []HistoryStat
	idMapCache        map[string]int
	searcherCache     *fuzzy.Searcher
	mu                sync.RWMutex
	historyLoadGate   = make(chan struct{}, 1)
	historySearchMu   sync.Mutex
	lastModTime       int64
	lastAtuinModTime  int64
	lastHistoryCheck  time.Time
	lastHistorySource string
	lastSearchQuery   string
	lastSearchAliases string
	lastSearchResults []HistResult
)

type sessionHistoryEntry struct {
	Command    string
	Cwd        string
	RecordedAt time.Time
}

func RecordSessionCommand(cmd string) {
	RecordSessionCommandAt(cmd, "")
}

func RecordSessionCommandAt(cmd, cwd string) {
	var recordable bool
	cmd, recordable = normalizeInteractiveHistoryCommand(cmd)
	if !recordable {
		return
	}
	sessionHistoryMu.Lock()
	if len(sessionHistory) > 0 && sessionHistory[len(sessionHistory)-1].Command == cmd {
		sessionHistoryMu.Unlock()
		return
	}
	sessionHistory = append(sessionHistory, sessionHistoryEntry{Command: cmd, Cwd: strings.TrimSpace(cwd), RecordedAt: time.Now()})
	sessionHistoryMu.Unlock()

	mu.Lock()
	historyCache = nil // invalidate to merge session history on next search
	historyStatsCache = nil
	lastHistoryCheck = time.Time{}
	resetIncrementalHistorySearchLocked()
	mu.Unlock()
}

type HistResult struct {
	ID         int
	Cmd        string
	FuzzyScore int
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
	limit := min(len(historyCache), 2000)
	// Published history snapshots are immutable. Pin their slice and map while
	// holding the read lock, then search them without copying thousands of
	// commands on every keystroke.
	commands := historyCache[:limit]
	ids := idMapCache
	mu.RUnlock()

	queries := []string{strings.ToLower(strings.TrimSpace(query))}
	for name, target := range aliases {
		name = strings.ToLower(strings.TrimSpace(name))
		target = strings.ToLower(strings.TrimSpace(target))
		if queries[0] == name && target != "" {
			queries = append(queries, target)
		} else if queries[0] == target && name != "" {
			queries = append(queries, name)
		}
	}

	results := make([]HistResult, 0, min(len(commands), 100))
	for _, command := range commands {
		lower := strings.ToLower(command)
		matched := false
		for _, candidate := range queries {
			if candidate == "" || strings.HasPrefix(lower, candidate) || strings.Contains(lower, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		results = append(results, HistResult{ID: ids[command], Cmd: command})
		if len(results) == 100 {
			break
		}
	}
	return results, true
}

func init() {
	idMapCache = make(map[string]int)
}

func ensureHistoryCache(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	shellName := "bash"
	if shell.Current != nil {
		shellName = shell.Current.GetName()
	}
	histFile := filepath.Join(home, ".bash_history")
	switch shellName {
	case "zsh":
		histFile = filepath.Join(home, ".zsh_history")
	case "fish":
		histFile = filepath.Join(home, ".local", "share", "fish", "fish_history")
	}
	atuinPath := atuinHistoryPath(home)
	sourceKey := shellName + "\x00" + histFile + "\x00" + atuinPath
	mu.RLock()
	recentlyChecked := historyCache != nil && lastHistorySource == sourceKey && time.Since(lastHistoryCheck) < 500*time.Millisecond
	mu.RUnlock()
	if recentlyChecked {
		return nil
	}
	historyModTime := maxFileModTime(histFile)
	atuinModTime := maxFileModTime(atuinPath, atuinPath+"-wal")

	cacheCurrent := func() bool {
		mu.RLock()
		defer mu.RUnlock()
		return historyCache != nil && lastHistorySource == sourceKey &&
			historyModTime <= lastModTime && atuinModTime <= lastAtuinModTime
	}
	if cacheCurrent() {
		mu.Lock()
		lastHistoryCheck = time.Now()
		lastHistorySource = sourceKey
		mu.Unlock()
		return nil
	}

	select {
	case historyLoadGate <- struct{}{}:
		defer func() { <-historyLoadGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if cacheCurrent() {
		mu.Lock()
		lastHistoryCheck = time.Now()
		lastHistorySource = sourceKey
		mu.Unlock()
		return nil
	}

	var persistent []historyOccurrence
	if atuin, atuinErr := loadAtuinHistoryContext(ctx, atuinPath); atuinErr == nil && len(atuin) > 0 {
		persistent = atuin
	} else {
		file, openErr := os.Open(histFile)
		if openErr != nil && !os.IsNotExist(openErr) {
			return openErr
		}
		if file != nil {
			persistent, err = loadShellHistoryContext(ctx, file, shellName)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	setPersistentHistoryEntries(historyOccurrencesToEntries(persistent))

	sessionHistoryMu.Lock()
	session := append([]sessionHistoryEntry(nil), sessionHistory...)
	sessionHistoryMu.Unlock()

	seen := make(map[string]bool)
	commands := make([]string, 0, len(session)+len(persistent))
	ids := make(map[string]int)
	currentID := len(session) + len(persistent)
	for index := len(session) - 1; index >= 0; index-- {
		command := session[index].Command
		if !seen[command] {
			commands = append(commands, command)
			seen[command] = true
			ids[command] = currentID
			currentID--
		}
	}
	for index := len(persistent) - 1; index >= 0; index-- {
		command := persistent[index].Command
		if !seen[command] {
			commands = append(commands, command)
			seen[command] = true
			ids[command] = currentID
			currentID--
		}
	}

	byCommandAndDirectory := make(map[string]*HistoryStat)
	for _, occurrence := range persistent {
		key := occurrence.Command + "\x00" + occurrence.Cwd
		stat := byCommandAndDirectory[key]
		if stat == nil {
			stat = &HistoryStat{
				Command: occurrence.Command, Cwd: occurrence.Cwd, LastUsed: occurrence.Timestamp,
				ExitCode: occurrence.ExitCode, HasExitCode: occurrence.HasExitCode,
				Duration: occurrence.Duration, Source: occurrence.Source,
			}
			byCommandAndDirectory[key] = stat
		}
		stat.Count++
		if occurrence.Timestamp.After(stat.LastUsed) {
			stat.LastUsed = occurrence.Timestamp
			stat.ExitCode = occurrence.ExitCode
			stat.HasExitCode = occurrence.HasExitCode
			stat.Duration = occurrence.Duration
		}
	}
	for _, entry := range session {
		key := entry.Command + "\x00" + entry.Cwd
		stat := byCommandAndDirectory[key]
		if stat == nil {
			stat = &HistoryStat{Command: entry.Command, Cwd: entry.Cwd, Source: "session"}
			byCommandAndDirectory[key] = stat
		}
		stat.Count++
		if entry.RecordedAt.After(stat.LastUsed) {
			stat.LastUsed = entry.RecordedAt
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
	searcher := fuzzy.NewPlainSearcher(commands)

	mu.Lock()
	historyCache = commands
	historyStatsCache = stats
	idMapCache = ids
	searcherCache = searcher
	lastModTime = historyModTime
	lastAtuinModTime = atuinModTime
	lastHistoryCheck = time.Now()
	lastHistorySource = sourceKey
	resetIncrementalHistorySearchLocked()
	mu.Unlock()
	return nil
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

	var results []HistResult
	seenCmds := make(map[string]bool)
	candidateHistory := history
	candidateSearcher := searcher
	aliasesKey := historyAliasesKey(aliases)
	historySearchMu.Lock()
	previousQuery := lastSearchQuery
	previousAliases := lastSearchAliases
	previousResults := append([]HistResult(nil), lastSearchResults...)
	historySearchMu.Unlock()
	if previousQuery != "" && strings.HasPrefix(strings.ToLower(query), strings.ToLower(previousQuery)) &&
		aliasesKey == previousAliases && len(previousResults) > 0 && len(previousResults) < 1000 {
		candidateHistory = make([]string, 0, len(previousResults))
		for _, result := range previousResults {
			candidateHistory = append(candidateHistory, result.Cmd)
		}
		candidateSearcher = fuzzy.NewPlainSearcher(candidateHistory)
	}

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

			seenCmds[cmd] = true
			results = append(results, HistResult{
				ID:         statsIDs[cmd],
				Cmd:        cmd,
				FuzzyScore: 10000,
			})
			strictMatches++
			if strictMatches >= 200 {
				break
			}
		}

		matches := candidateSearcher.SearchWithScores(q, &fuzzy.SearchOptions{Limit: 1000})
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

	tiers := make([]int, len(results))
	for i, r := range results {
		tiers[i] = getTier(r.Cmd, query)
	}

	sort.SliceStable(results, func(i, j int) bool {
		tI := tiers[i]
		tJ := tiers[j]
		if tI != tJ {
			return tI < tJ
		}

		if tI == 4 && results[i].FuzzyScore != results[j].FuzzyScore {
			return results[i].FuzzyScore > results[j].FuzzyScore
		}

		return results[i].ID > results[j].ID
	})

	historySearchMu.Lock()
	lastSearchQuery = query
	lastSearchAliases = aliasesKey
	lastSearchResults = append([]HistResult(nil), results...)
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
	historySearchMu.Unlock()
}

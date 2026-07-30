package integration

import (
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
	mu                sync.Mutex
	lastModTime       int64
	lastAtuinModTime  int64
)

type sessionHistoryEntry struct {
	Command string
	Cwd     string
}

func RecordSessionCommand(cmd string) {
	RecordSessionCommandAt(cmd, "")
}

func RecordSessionCommandAt(cmd, cwd string) {
	cmd = strings.TrimSpace(cmd)
	if !isSafeInteractiveHistoryCommand(cmd) {
		return
	}
	mu.Lock()
	defer mu.Unlock()

	sessionHistoryMu.Lock()
	defer sessionHistoryMu.Unlock()

	if len(sessionHistory) > 0 && sessionHistory[len(sessionHistory)-1].Command == cmd {
		return
	}
	sessionHistory = append(sessionHistory, sessionHistoryEntry{Command: cmd, Cwd: strings.TrimSpace(cwd)})
	historyCache = nil // invalidate to merge session history on next search
	historyStatsCache = nil
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
	mu.Lock()
	defer mu.Unlock()

	return append([]HistoryStat(nil), historyStatsCache...)
}

func init() {
	idMapCache = make(map[string]int)
}

func SearchHistory(query string, aliases map[string]string) ([]HistResult, error) {
	mu.Lock()
	defer mu.Unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	shellName := "bash"
	if shell.Current != nil {
		shellName = shell.Current.GetName()
	}

	var histFile string
	switch shellName {
	case "zsh":
		histFile = filepath.Join(home, ".zsh_history")
	case "fish":
		histFile = filepath.Join(home, ".local", "share", "fish", "fish_history")
	default:
		histFile = filepath.Join(home, ".bash_history")
	}

	if info, err := os.Stat(histFile); err == nil {
		if info.ModTime().UnixNano() > lastModTime {
			historyCache = nil // force reload
			historyStatsCache = nil
			idMapCache = make(map[string]int)
			lastModTime = info.ModTime().UnixNano()
		}
	}
	atuinPath := atuinHistoryPath(home)
	if modTime := maxFileModTime(atuinPath, atuinPath+"-wal"); modTime > lastAtuinModTime {
		historyCache = nil
		historyStatsCache = nil
		idMapCache = make(map[string]int)
		lastAtuinModTime = modTime
	}

	// lazy load history if cache is empty
	if len(historyCache) == 0 {
		file, err := os.Open(histFile)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if file != nil {
			defer func() { _ = file.Close() }()
		}

		var persistent []historyOccurrence
		if atuin, atuinErr := loadAtuinHistory(atuinPath); atuinErr == nil && len(atuin) > 0 {
			persistent = atuin
		} else if file != nil {
			persistent, err = loadShellHistory(file, shellName)
			if err != nil {
				return nil, err
			}
		}
		setPersistentHistoryEntries(historyOccurrencesToEntries(persistent))

		// build historyCache backwards so newest commands come first
		seen := make(map[string]bool)
		historyCache = nil
		historyStatsCache = nil
		idMapCache = make(map[string]int)

		sessionHistoryMu.Lock()
		currentID := len(sessionHistory) + len(persistent)
		for i := len(sessionHistory) - 1; i >= 0; i-- {
			cmd := sessionHistory[i].Command
			if !seen[cmd] {
				historyCache = append(historyCache, cmd)
				seen[cmd] = true
				idMapCache[cmd] = currentID
				currentID--
			}
		}
		sessionHistoryMu.Unlock()

		for i := len(persistent) - 1; i >= 0; i-- {
			cmd := persistent[i].Command
			if !seen[cmd] {
				historyCache = append(historyCache, cmd)
				seen[cmd] = true
				idMapCache[cmd] = currentID
				currentID--
			}
		}

		now := time.Now()
		stats := make(map[string]*HistoryStat)
		for index, occurrence := range persistent {
			lastUsed := occurrence.Timestamp
			if lastUsed.IsZero() {
				lastUsed = now.Add(-time.Duration(len(persistent)-index) * time.Second)
			}
			key := occurrence.Command + "\x00" + occurrence.Cwd
			stat := stats[key]
			if stat == nil {
				stat = &HistoryStat{
					Command:     occurrence.Command,
					Cwd:         occurrence.Cwd,
					LastUsed:    lastUsed,
					ExitCode:    occurrence.ExitCode,
					HasExitCode: occurrence.HasExitCode,
					Duration:    occurrence.Duration,
					Source:      occurrence.Source,
				}
				stats[key] = stat
			}
			stat.Count++
			if lastUsed.After(stat.LastUsed) {
				stat.LastUsed = lastUsed
				stat.ExitCode = occurrence.ExitCode
				stat.HasExitCode = occurrence.HasExitCode
				stat.Duration = occurrence.Duration
			}
		}
		sessionHistoryMu.Lock()
		for index, entry := range sessionHistory {
			key := entry.Command + "\x00" + entry.Cwd
			stat := stats[key]
			if stat == nil {
				stat = &HistoryStat{Command: entry.Command, Cwd: entry.Cwd, Source: "session"}
				stats[key] = stat
			}
			stat.Count++
			stat.LastUsed = now.Add(-time.Duration(len(sessionHistory)-index) * time.Millisecond)
		}
		sessionHistoryMu.Unlock()
		for _, stat := range stats {
			historyStatsCache = append(historyStatsCache, *stat)
		}
		sort.SliceStable(historyStatsCache, func(i, j int) bool {
			return historyStatsCache[i].LastUsed.After(historyStatsCache[j].LastUsed)
		})

		searcherCache = fuzzy.NewPlainSearcher(historyCache)
	}

	if query == "" {
		var results []HistResult
		limit := min(len(historyCache), 100)

		for i := range limit {
			cmd := historyCache[i]
			results = append(results, HistResult{
				ID:  idMapCache[cmd],
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

	addMatches := func(q string, subcmdFilter bool) {
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
		for _, cmd := range historyCache {
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
				ID:         idMapCache[cmd],
				Cmd:        cmd,
				FuzzyScore: 10000,
			})
			strictMatches++
			if strictMatches >= 200 {
				break
			}
		}

		matches := searcherCache.SearchWithScores(q, &fuzzy.SearchOptions{Limit: 1000})
		for _, m := range matches {
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
				ID:         idMapCache[m.Str],
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

	return results, nil
}

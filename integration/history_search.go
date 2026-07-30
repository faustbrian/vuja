package integration

import (
	"path/filepath"
	"strings"
)

type HistoryScope string

const (
	HistoryScopeDirectory HistoryScope = "directory"
	HistoryScopeProject   HistoryScope = "project"
	HistoryScopeGlobal    HistoryScope = "global"
	HistoryScopeMachine   HistoryScope = "machine"
	HistoryScopeSession   HistoryScope = "session"
)

type HistorySearchOptions struct {
	Cwd            string
	ProjectRoot    string
	Scope          HistoryScope
	SuccessfulOnly bool
	Limit          int
}

func SearchHistoryWithOptions(query string, aliases map[string]string, options HistorySearchOptions) ([]HistResult, error) {
	results, err := SearchHistory(query, aliases)
	if err != nil {
		return nil, err
	}
	filtered := filterHistoryResults(results, HistorySnapshot(), options)
	if options.Limit > 0 && len(filtered) > options.Limit {
		filtered = filtered[:options.Limit]
	}
	return filtered, nil
}

func filterHistoryResults(results []HistResult, stats []HistoryStat, options HistorySearchOptions) []HistResult {
	statsByCommand := make(map[string][]HistoryStat, len(stats))
	for _, stat := range stats {
		statsByCommand[stat.Command] = append(statsByCommand[stat.Command], stat)
	}

	filtered := make([]HistResult, 0, len(results))
	for _, result := range results {
		commandStats := statsByCommand[result.Cmd]
		matchesScope := options.Scope == HistoryScopeGlobal
		matchesSuccess := !options.SuccessfulOnly
		for _, stat := range commandStats {
			if historyStatMatchesScope(stat, options) {
				matchesScope = true
				if stat.HasExitCode && stat.ExitCode == 0 {
					matchesSuccess = true
				}
			}
		}
		if matchesScope && matchesSuccess {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func historyStatMatchesScope(stat HistoryStat, options HistorySearchOptions) bool {
	switch options.Scope {
	case HistoryScopeDirectory:
		return filepath.Clean(stat.Cwd) == filepath.Clean(options.Cwd)
	case HistoryScopeProject:
		if strings.TrimSpace(options.ProjectRoot) == "" || strings.TrimSpace(stat.Cwd) == "" {
			return false
		}
		relative, err := filepath.Rel(options.ProjectRoot, stat.Cwd)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	default:
		return true
	}
}

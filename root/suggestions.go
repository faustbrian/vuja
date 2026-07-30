package root

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/ai"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
)

// MergeResults collects and dedupes suggestions for a query and mode
func MergeResults(query string, mode string) []spec.Suggestion {
	scored := scoreResults(query, mode)
	maxSugg := config.Get().UI.MaxSuggestions

	finalResults := make([]spec.Suggestion, 0, min(len(scored), maxSugg))
	for _, result := range scored {
		finalResults = append(finalResults, result.Suggestion)
		if len(finalResults) == maxSugg {
			break
		}
	}
	return finalResults
}

func scoreResults(query string, mode string) []scoring.ScoredSuggestion {
	segment := spec.ActiveCommandSegment(query)
	activeQuery := segment.Query
	normalizedQuery := strings.TrimSpace(activeQuery)
	cwd := spec.GetCWD()

	// always call lookup to scan aliases and get spec suggestions
	logger.Debugf("Merge Calling Lookup for '%s'", query)
	cmdResults := spec.Lookup(activeQuery)
	histResults, _ := integration.SearchHistory(activeQuery, spec.GetAliasesCopy())
	deduped := collectSuggestionCandidates(activeQuery, cmdResults, histResults)
	ws := workspace.DetectCached(cwd)
	store, _ := scoring.GetFrecencyStore()
	deduped = append(deduped, gitStateSuggestions(activeQuery, ws)...)
	deduped = append(deduped, discoverProjectCommands(activeQuery, ws)...)
	deduped = append(deduped, indexedDirectorySuggestions(activeQuery, cwd)...)
	ctxArguments, cancelArguments := context.WithTimeout(context.Background(), 20*time.Millisecond)
	deduped = append(deduped, learnedArgumentSuggestions(ctxArguments, activeQuery, cwd, ws.Root, store)...)
	cancelArguments()
	ctxRecovery, cancelRecovery := context.WithTimeout(context.Background(), 20*time.Millisecond)
	deduped = append(deduped, failureRecoverySuggestions(ctxRecovery, activeQuery, cwd, store, spec.NearestExternalCommands)...)
	cancelRecovery()
	deduped = dedupeSuggestionCandidates(activeQuery, deduped)
	deduped = applySuggestionPolicy(activeQuery, deduped)
	if segment.Prefix != "" {
		for i := range deduped {
			deduped[i].Cmd = segment.Prefix + deduped[i].Cmd
		}
		normalizedQuery = strings.TrimSpace(segment.Prefix + normalizedQuery)
	}
	if mode == "history" {
		for i := range deduped {
			if deduped[i].Source == "history" {
				deduped[i].Confidence = min(deduped[i].Confidence+20, 100)
			}
		}
	}

	seen := make(map[string]bool, len(deduped))
	for _, suggestion := range deduped {
		seen[strings.TrimSpace(suggestion.Cmd)] = true
	}
	injectAISuggestion(&deduped, seen, normalizedQuery)

	tokens := spec.Tokenize(activeQuery)
	rootCmd := ""
	if len(tokens) > 0 {
		rootCmd = tokens[0]
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	prevCommand, prevSkeleton := getPrevCommandSignals()
	signals := scoring.CollectSignals(ctxTimeout, cwd, normalizedQuery, rootCmd, store, prevCommand, prevSkeleton)
	return promoteAISuggestion(scoring.Score(deduped, signals))
}

func promoteAISuggestion(scored []scoring.ScoredSuggestion) []scoring.ScoredSuggestion {
	for index := range scored {
		if scored[index].Source != "ai" {
			continue
		}
		suggestion := scored[index]
		copy(scored[1:index+1], scored[:index])
		scored[0] = suggestion
		break
	}
	return scored
}

func dedupeSuggestionCandidates(query string, suggestions []spec.Suggestion) []spec.Suggestion {
	query = strings.TrimSpace(query)
	positions := make(map[string]int, len(suggestions))
	deduped := make([]spec.Suggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		suggestion.Cmd = strings.TrimSpace(suggestion.Cmd)
		if suggestion.Cmd == "" || suggestion.Cmd == query {
			continue
		}
		if position, ok := positions[suggestion.Cmd]; ok {
			if suggestion.Priority > deduped[position].Priority {
				deduped[position] = suggestion
			}
			continue
		}
		positions[suggestion.Cmd] = len(deduped)
		deduped = append(deduped, suggestion)
	}
	return deduped
}

func applySuggestionPolicy(query string, suggestions []spec.Suggestion) []spec.Suggestion {
	cfg := config.Get().Suggestions
	seen := make(map[string]bool, len(suggestions))
	filtered := make([]spec.Suggestion, 0, len(suggestions)+len(cfg.Pins))
	for _, suggestion := range suggestions {
		command := strings.TrimSpace(suggestion.Cmd)
		if command == "" || policy.IsSensitive(command) || policy.Blocked(command) {
			continue
		}
		if cfg.SuppressDestructive && policy.IsDestructive(command) && !policy.IsDestructive(query) {
			continue
		}
		seen[command] = true
		filtered = append(filtered, suggestion)
	}
	for _, command := range cfg.Pins {
		command = strings.TrimSpace(command)
		if command == "" || seen[command] || policy.IsSensitive(command) || policy.Blocked(command) {
			continue
		}
		if query == "" || strings.HasPrefix(strings.ToLower(command), strings.ToLower(query)) {
			filtered = append(filtered, spec.Suggestion{Cmd: command, Desc: "pinned", Source: "pin", Priority: 100})
		}
	}
	return filtered
}

func gitStateSuggestions(query string, ws workspace.WorkspaceInfo) []spec.Suggestion {
	if !ws.HasGit || (query != "" && !strings.HasPrefix("git ", strings.ToLower(query)) && !strings.HasPrefix(strings.ToLower(query), "git ")) {
		return nil
	}
	ws = workspace.WithGitStatusCached(ws)
	var commands []string
	switch {
	case ws.GitConflicted:
		commands = []string{"git status", "git diff --name-only --diff-filter=U"}
	case ws.GitRebasing:
		commands = []string{"git rebase --continue", "git rebase --abort"}
	case ws.GitMerging:
		commands = []string{"git merge --continue", "git merge --abort"}
	case ws.GitCherryPicking:
		commands = []string{"git cherry-pick --continue", "git cherry-pick --abort"}
	case ws.GitReverting:
		commands = []string{"git revert --continue", "git revert --abort"}
	default:
		if ws.GitDirty {
			commands = append(commands, "git diff", "git add -A")
		}
		if ws.GitStaged {
			commands = append(commands, "git diff --cached", "git commit")
		}
		if ws.GitAhead > 0 {
			commands = append(commands, "git push")
		}
		if ws.GitBehind > 0 {
			commands = append(commands, "git pull --ff-only")
		}
		if len(commands) == 0 {
			return nil
		}
	}
	results := make([]spec.Suggestion, 0, len(commands))
	for _, command := range commands {
		if query == "" || strings.HasPrefix(command, query) {
			results = append(results, spec.Suggestion{Cmd: command, Desc: "active git operation", Source: "workspace", Priority: 95})
		}
	}
	return results
}

func collectSuggestionCandidates(query string, commandResults []spec.Suggestion, historyResults []integration.HistResult) []spec.Suggestion {
	normalizedQuery := strings.TrimSpace(query)
	seen := make(map[string]bool)
	candidates := make([]spec.Suggestion, 0, len(commandResults)+len(historyResults))

	add := func(suggestion spec.Suggestion) {
		suggestion.Cmd = strings.TrimSpace(suggestion.Cmd)
		if suggestion.Cmd == "" || suggestion.Cmd == normalizedQuery || seen[suggestion.Cmd] {
			return
		}
		if suggestion.Source == "" {
			suggestion.Source = "spec"
		}
		if suggestion.Confidence == 0 {
			suggestion.Confidence = 50
		}
		seen[suggestion.Cmd] = true
		candidates = append(candidates, suggestion)
	}

	for _, result := range commandResults {
		add(result)
	}
	for i, result := range historyResults {
		add(spec.Suggestion{
			Cmd:        result.Cmd,
			Desc:       "history",
			Icon:       "history",
			Source:     "history",
			Confidence: max(55-(i*2), 40),
		})
	}
	return candidates
}

func importPersistentHistory() {
	if _, err := integration.SearchHistory("", nil); err != nil {
		return
	}
	stats := integration.HistorySnapshot()
	entries := make([]scoring.ImportedHistoryEntry, 0, len(stats))
	for _, stat := range stats {
		if policy.IsSensitive(stat.Command) {
			continue
		}
		entries = append(entries, scoring.ImportedHistoryEntry{
			Command:     stat.Command,
			Cwd:         stat.Cwd,
			Count:       stat.Count,
			LastUsed:    stat.LastUsed,
			ExitCode:    stat.ExitCode,
			HasExitCode: stat.HasExitCode,
			Duration:    stat.Duration,
			Source:      stat.Source,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if store, err := scoring.GetFrecencyStore(); err == nil {
		_ = store.ReplaceImportedHistory(ctx, entries)
		persistentExecutions := integration.PersistentHistoryEntriesSnapshot()
		historyEvents := make([]scoring.HistoryEvent, 0, len(persistentExecutions))
		for _, execution := range persistentExecutions {
			if policy.IsSensitive(execution.Command) {
				continue
			}
			historyEvents = append(historyEvents, scoring.HistoryEvent{
				EventKey:    execution.ID,
				Command:     execution.Command,
				Cwd:         execution.Cwd,
				StartedAt:   execution.StartedAt,
				Duration:    execution.Duration,
				ExitCode:    execution.ExitCode,
				HasExitCode: execution.HasExitCode,
				Source:      execution.Source,
				Host:        execution.Host,
				SessionID:   execution.SessionID,
				Imported:    true,
			})
		}
		_ = store.ReplaceImportedHistoryEvents(ctx, historyEvents)
		if storedEvents, queryErr := store.QueryHistoryEvents(ctx, 10_000); queryErr == nil {
			richEntries := make([]integration.HistoryEntry, 0, len(storedEvents))
			for _, event := range storedEvents {
				richEntries = append(richEntries, integration.HistoryEntry{
					ID:          event.EventKey,
					Command:     event.Command,
					Cwd:         event.Cwd,
					StartedAt:   event.StartedAt,
					Duration:    event.Duration,
					ExitCode:    event.ExitCode,
					HasExitCode: event.HasExitCode,
					Source:      event.Source,
					Host:        event.Host,
					SessionID:   event.SessionID,
				})
			}
			integration.ReplaceRichHistoryEntries(richEntries)
		}
		historyDirectories := historyDirectoryImports(stats)
		_ = store.ReplaceDirectorySource(ctx, "history", historyDirectories)
		zoxideCtx, zoxideCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		zoxideDirectories := loadZoxideDirectories(zoxideCtx)
		zoxideCancel()
		_ = store.ReplaceDirectorySource(ctx, "zoxide", zoxideDirectories)
		_ = store.ReplaceDirectorySource(
			ctx,
			"git-worktrees",
			gitWorktreeImports(spec.GetCWD(), historyDirectories),
		)
	}
}

func injectAISuggestion(deduped *[]spec.Suggestion, seen map[string]bool, normalizedQuery string) {
	if aiSugg := GetCurrentAISuggestion(); aiSugg != nil {
		normalizedCmd := strings.TrimSpace(aiSugg.Cmd)
		if normalizedCmd != "" && normalizedCmd != normalizedQuery && strings.HasPrefix(strings.ToLower(normalizedCmd), strings.ToLower(normalizedQuery)) {
			if !seen[aiSugg.Cmd] {
				seen[aiSugg.Cmd] = true
				*deduped = append(*deduped, *aiSugg)
			} else {
				for i, item := range *deduped {
					if item.Cmd == aiSugg.Cmd && aiSugg.Confidence > item.Confidence {
						(*deduped)[i].Confidence = aiSugg.Confidence
						if (*deduped)[i].Source == "" || (*deduped)[i].Source == "spec" || (*deduped)[i].Source == "history" {
							(*deduped)[i].Source = "ai"
						}
						break
					}
				}
			}
		}
	}
}

var (
	aiEngine     *ai.AIEngine
	aiEngineOnce sync.Once
)

func GetAIEngine() *ai.AIEngine {
	aiEngineOnce.Do(func() {
		aiEngine = ai.NewAIEngine(nil)
		for _, p := range ai.DefaultProviders {
			aiEngine.RegisterProvider(p)
		}
	})
	return aiEngine
}

var (
	currentAISugg *spec.Suggestion
	aiSuggMu      sync.RWMutex
)

func SetCurrentAISuggestion(sugg *spec.Suggestion) {
	aiSuggMu.Lock()
	defer aiSuggMu.Unlock()
	currentAISugg = sugg
}

func GetCurrentAISuggestion() *spec.Suggestion {
	aiSuggMu.RLock()
	defer aiSuggMu.RUnlock()
	return currentAISugg
}

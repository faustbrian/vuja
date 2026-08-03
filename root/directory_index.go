package root

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

func indexedDirectorySuggestions(ctx context.Context, query, cwd string) []spec.Suggestion {
	if persistentHistoryImporting.Load() {
		return nil
	}
	fields := strings.Fields(query)
	if len(fields) == 0 || (fields[0] != "cd" && fields[0] != "z") {
		return nil
	}
	fragment := ""
	if len(fields) > 1 {
		fragment = fields[len(fields)-1]
	}
	lookupFragment := fragment
	if lookupFragment == "" {
		lookupFragment = filepath.Clean(cwd) + string(filepath.Separator)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		return nil
	}
	paths, err := store.QueryDirectories(ctx, lookupFragment, 20, config.Get().Suggestions.DirectoryRanking)
	if err != nil {
		return nil
	}
	var results []spec.Suggestion
	for index, entry := range paths {
		target := entry.Path
		if relative, relErr := filepath.Rel(cwd, entry.Path); relErr == nil && relative != "." && !strings.HasPrefix(relative, "..") {
			target = relative
		}
		command := fields[0] + " " + target
		if strings.HasPrefix(strings.ToLower(command), strings.ToLower(query)) || strings.Contains(strings.ToLower(target), strings.ToLower(fragment)) {
			results = append(results, spec.Suggestion{Cmd: command, Desc: "visited directory", Source: "directory-index", Priority: max(100-index, 1)})
		}
	}
	return results
}

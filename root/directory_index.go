package root

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

func indexedDirectorySuggestions(query, cwd string) []spec.Suggestion {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		return nil
	}
	paths, err := store.QueryDirectories(ctx, fragment, 20)
	if err != nil {
		return nil
	}
	var results []spec.Suggestion
	for _, path := range paths {
		target := path
		if relative, relErr := filepath.Rel(cwd, path); relErr == nil && relative != "." && !strings.HasPrefix(relative, "..") {
			target = relative
		}
		command := fields[0] + " " + target
		if strings.HasPrefix(strings.ToLower(command), strings.ToLower(query)) || strings.Contains(strings.ToLower(target), strings.ToLower(fragment)) {
			results = append(results, spec.Suggestion{Cmd: command, Desc: "visited directory", Source: "directory-index", Priority: 85})
		}
	}
	return results
}

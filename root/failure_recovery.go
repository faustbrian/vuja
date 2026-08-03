package root

import (
	"context"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

type nearestCommandFinder func(input string, limit int) []string

func failureRecoverySuggestions(
	ctx context.Context,
	query string,
	cwd string,
	store *scoring.FrecencyStore,
	findNearest nearestCommandFinder,
) []spec.Suggestion {
	if store == nil {
		return nil
	}
	failure, ok := store.QueryRecentFailure(ctx, cwd, 2*time.Minute)
	if !ok {
		return nil
	}
	fields := strings.Fields(failure.Command)
	if len(fields) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	seen := make(map[string]bool)
	var suggestions []spec.Suggestion
	add := func(command, description string, priority int) {
		command = strings.TrimSpace(command)
		if command == "" || command == failure.Command || seen[command] {
			return
		}
		if query != "" && !strings.HasPrefix(strings.ToLower(command), strings.ToLower(query)) {
			return
		}
		seen[command] = true
		suggestions = append(suggestions, spec.Suggestion{
			Cmd:        command,
			Desc:       description,
			Source:     "recovery",
			Priority:   priority,
			Confidence: 95,
		})
	}

	if failure.ExitCode == 127 && findNearest != nil {
		for _, command := range findNearest(fields[0], 5) {
			if spec.CommandEditDistance(fields[0], command) > 2 {
				continue
			}
			replacement := append([]string{command}, fields[1:]...)
			add(strings.Join(replacement, " "), "command correction", 100)
		}
	}

	for _, entry := range mustQueryLocalHistory(ctx, store, cwd, fields[0]) {
		if entry.Count > 0 {
			add(entry.Cmd, "recent successful variant", 95)
		}
	}
	return suggestions
}

func mustQueryLocalHistory(ctx context.Context, store *scoring.FrecencyStore, cwd, prefix string) []scoring.FrecencyEntry {
	entries, err := store.QueryLocal(ctx, cwd, prefix, 20)
	if err != nil {
		return nil
	}
	return entries
}

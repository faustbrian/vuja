package root

import (
	"context"
	"strings"

	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

func learnedArgumentSuggestions(
	ctx context.Context,
	query string,
	cwd string,
	root string,
	store *scoring.FrecencyStore,
) []spec.Suggestion {
	if store == nil {
		return nil
	}
	tokens := spec.Tokenize(query)
	if len(tokens) < 2 {
		return nil
	}
	position := len(tokens) - 1
	partial := tokens[position]
	scope := strings.TrimSpace(strings.Join(tokens[:position], " "))
	if scope == "" {
		return nil
	}
	values, err := store.QueryArgumentValues(ctx, cwd, root, scope, position, partial, 20)
	if err != nil {
		return nil
	}
	prefix := query
	if partial != "" && strings.HasSuffix(query, partial) {
		prefix = query[:len(query)-len(partial)]
	}
	suggestions := make([]spec.Suggestion, 0, len(values))
	for _, entry := range values {
		if entry.Value == partial {
			continue
		}
		suggestions = append(suggestions, spec.Suggestion{
			Cmd:        prefix + quoteShellArgument(entry.Value),
			Desc:       "learned argument",
			Source:     "argument",
			Priority:   80 + min(entry.Affinity*5, 15),
			Confidence: 80,
		})
	}
	return suggestions
}

func quoteShellArgument(value string) string {
	if !strings.ContainsAny(value, " \t'\"") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

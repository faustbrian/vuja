package root

import (
	"fmt"
	"slices"
	"strings"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
)

// These are the scorer's actual normalized signals, not execution counts,
// elapsed recency, confidence or probabilities. Keep placement rules visible
// because directory/AI/history-recall placement can override the weighted score.
func rankingExplanation(result scoring.ScoredSuggestion, query string) string {
	parts := []string{}
	pinned := slices.ContainsFunc(config.Get().Suggestions.Pins, func(pin string) bool {
		return strings.TrimSpace(pin) == strings.TrimSpace(result.Cmd)
	})
	if pinned {
		parts = append(parts, "pinned priority")
	}
	if result.Source == "ai" {
		parts = append(parts, "AI placement")
	} else if scoring.VerifiedPathPrefix(result.Suggestion, query) {
		parts = append(parts, "verified path prefix")
	} else if rank := directoryPolicyRank(result.Suggestion); !pinned && rank > 0 {
		fields := strings.Fields(query)
		if len(fields) > 0 && (fields[0] == "cd" || fields[0] == "z") {
			parts = append(parts, fmt.Sprintf("directory %s #%d", config.Get().Suggestions.DirectoryRanking, rank))
		}
	}
	parts = append(parts, fmt.Sprintf("score %.2f", result.Score))
	b := result.Breakdown
	for _, signal := range []struct {
		name  string
		value int
	}{
		{"match", b.MatchQuality}, {"usage signal", b.Frecency},
		{"previous-command signal", b.Transition}, {"context", b.ContextBonus},
		{"directory signal", b.DirectoryAffinity}, {"feedback", b.Feedback},
		{"outcome", b.Outcome}, {"priority", b.BasePriority},
	} {
		if signal.value != 0 {
			parts = append(parts, fmt.Sprintf("%s %d", signal.name, signal.value))
		}
	}
	return strings.Join(parts, " • ")
}

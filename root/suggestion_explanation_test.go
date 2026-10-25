package root

import (
	"strings"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/faustbrian/vuja/v2/internal/scoring"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestVisibleSuggestionsCarryActualRankingContributions(t *testing.T) {
	results := limitScoredSuggestions([]scoring.ScoredSuggestion{{
		Suggestion: spec.Suggestion{Cmd: "git status", Source: "spec"},
		Score:      12.5, Breakdown: scoring.ScoreBreakdown{BasePriority: 50, MatchQuality: 40, Frecency: 20},
	}}, 10, "git")
	got := results[0].RankingExplanation
	for _, expected := range []string{"score 12.50", "match 40", "usage signal 20", "priority 50"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("ranking contribution %q was lost before presentation: %q", expected, got)
		}
	}
	for _, invented := range []string{"uses", "ago", "%", "confidence"} {
		if strings.Contains(got, invented) {
			t.Fatalf("explanation invented usage/recency/confidence: %q", got)
		}
	}
}

func TestPinAppliesToAnAlreadyDiscoveredCandidate(t *testing.T) {
	before := config.Get()
	cfg := config.DefaultConfig()
	cfg.Suggestions.Pins = []string{"git status"}
	config.Init(cfg)
	t.Cleanup(func() { config.Init(before) })
	got := applySuggestionPolicy("git", []spec.Suggestion{{ID: "status", Cmd: "git status", Source: "history", Confidence: 60}})
	if len(got) != 1 || got[0].Priority != 100 || got[0].ID != "status" || got[0].Source != "history" {
		t.Fatalf("pin did not apply while preserving provider identity and source: %+v", got)
	}
}

func TestRankingExplanationDoesNotInventDirectoryRankForAPin(t *testing.T) {
	before := config.Get()
	cfg := config.DefaultConfig()
	cfg.Suggestions.Pins = []string{" cd api/ "}
	config.Init(cfg)
	t.Cleanup(func() { config.Init(before) })
	got := rankingExplanation(scoring.ScoredSuggestion{
		Suggestion: spec.Suggestion{Cmd: "cd api/", Source: "directory-index", Priority: 100},
		Score:      12.5,
	}, "cd")
	if !strings.Contains(got, "pinned priority") || strings.Contains(got, "directory balanced #1") {
		t.Fatalf("pin priority was mislabeled as directory-ranking evidence: %q", got)
	}
}

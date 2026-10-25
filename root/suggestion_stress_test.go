package root

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/scoring"
	"github.com/faustbrian/vuja/v2/spec"
)

// QA scenario 8: provider enumeration is not ranking evidence. Keep meaningful
// evidence equal, vary only enumeration, and assert independently known order
// and identity through collection, deduplication and the real scorer.
func TestSuggestionRankingTiesIgnoreProviderEnumeration(t *testing.T) {
	want := make([]spec.Suggestion, 64)
	for i := range want {
		want[i] = spec.Suggestion{
			ID: fmt.Sprintf("fixture:%03d", i), Cmd: fmt.Sprintf("ssh forge@fixture-%03d", i),
			Source: "spec", Priority: 50, Confidence: 50,
		}
	}
	rng := rand.New(rand.NewSource(17))
	for iteration := range 128 {
		commands := append([]spec.Suggestion(nil), want...)
		rng.Shuffle(len(commands), func(i, j int) { commands[i], commands[j] = commands[j], commands[i] })
		candidates := collectSuggestionCandidates("ssh", commands, nil)
		candidates = dedupeSuggestionCandidates("ssh", candidates)
		got := scoring.ScoreWithConfig(candidates, scoring.SignalSet{Query: "ssh"}, scoring.DefaultScoreConfig)
		if len(got) != len(want) {
			t.Fatalf("permutation %d lost suggestions: got %d, want %d", iteration, len(got), len(want))
		}
		for i := range want {
			if got[i].Cmd != want[i].Cmd || got[i].ID != want[i].ID {
				t.Fatalf("permutation %d rank %d changed command/identity: got %q/%q, want %q/%q", iteration, i, got[i].Cmd, got[i].ID, want[i].Cmd, want[i].ID)
			}
		}
	}
}

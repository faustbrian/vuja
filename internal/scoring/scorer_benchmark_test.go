package scoring

import (
	"fmt"
	"testing"

	"github.com/faustbrian/vuja/spec"
)

func BenchmarkScoreCandidates(b *testing.B) {
	const candidateCount = 200
	suggestions := make([]spec.Suggestion, 0, candidateCount)
	local := make([]FrecencyEntry, 0, candidateCount/4)
	global := make([]FrecencyEntry, 0, candidateCount/2)
	for index := range candidateCount {
		command := fmt.Sprintf("git command-%03d --flag", index)
		source := "spec"
		if index%4 == 0 {
			source = "history"
			local = append(local, FrecencyEntry{Cmd: command, RawScore: float64(candidateCount - index)})
		}
		if index%2 == 0 {
			global = append(global, FrecencyEntry{Cmd: command, RawScore: float64(candidateCount - index)})
		}
		suggestions = append(suggestions, spec.Suggestion{
			Cmd:        command,
			Source:     source,
			Confidence: 55,
			Priority:   60,
		})
	}
	signals := SignalSet{
		Query:          "git command-",
		LocalFrecency:  local,
		GlobalFrecency: global,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result := Score(suggestions, signals)
		if len(result) != candidateCount {
			b.Fatalf("expected %d candidates, got %d", candidateCount, len(result))
		}
	}
}

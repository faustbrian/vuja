package root

import (
	"strings"
	"testing"

	"github.com/faustbrian/vuja/integration"
)

func TestHistoryExplainReportsTheActualPostRankingCount(t *testing.T) {
	output := formatHistoryExplanation(
		integration.CanonicalHistoryCounts{Events: 80, Candidates: 60, Eligible: 40},
		integration.HistorySearchStatus{Truncated: true},
		12,
		5,
	)
	if !strings.Contains(output, "after-filter 40\nafter-ranking 12\nreturned 5\n") {
		t.Fatalf("expected distinct filtering and ranking counts, got %q", output)
	}
	if !strings.Contains(output, "truncated true\n") {
		t.Fatalf("expected truncation state, got %q", output)
	}
}

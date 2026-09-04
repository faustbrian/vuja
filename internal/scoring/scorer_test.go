package scoring

import (
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
)

func TestScore_GitInitAndStatusInGitRepo(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "git init", Source: "spec"},
		{Cmd: "git status -s", Source: "spec"},
	}
	signals := SignalSet{
		Workspace: workspace.WorkspaceInfo{HasGit: true},
		Query:     "git",
	}

	scored := Score(suggestions, signals)
	if len(scored) != 2 {
		t.Fatalf("expected 2 scored suggestions, got %d", len(scored))
	}
	if scored[0].Cmd != "git status -s" {
		t.Errorf("expected 'git status -s' at top when inside git repo, got %s", scored[0].Cmd)
	}
	if scored[1].Cmd != "git init" {
		t.Errorf("expected 'git init' at bottom, got %s", scored[1].Cmd)
	}
	if scored[1].Breakdown.ContextBonus != -50 {
		t.Errorf("expected -50 penalty for git init, got %d", scored[1].Breakdown.ContextBonus)
	}
}

func TestScore_NormalizedFrecency(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "ls -la", Source: "history"},
		{Cmd: "git push", Source: "history"},
	}

	now := time.Now()
	signals := SignalSet{
		LocalFrecency: []FrecencyEntry{
			{Cmd: "ls -la", RawScore: 25000.0, LastUsed: now.Add(-30 * 24 * time.Hour)},
			{Cmd: "git push", RawScore: 500.0, LastUsed: now},
		},
	}

	scored := Score(suggestions, signals)
	var lsBreakdown, pushBreakdown ScoreBreakdown
	for _, s := range scored {
		switch s.Cmd {
		case "ls -la":
			lsBreakdown = s.Breakdown
		case "git push":
			pushBreakdown = s.Breakdown
		}
	}

	if lsBreakdown.Frecency <= pushBreakdown.Frecency || lsBreakdown.Frecency > 100 {
		t.Errorf("expected the stronger absolute signal to rank higher, got ls=%d push=%d", lsBreakdown.Frecency, pushBreakdown.Frecency)
	}
	if pushBreakdown.Frecency <= 0 || pushBreakdown.Frecency > 100 {
		t.Errorf("expected normalized frecency in (0, 100], got %d", pushBreakdown.Frecency)
	}
}

func TestScore_FrecencyNormalizationDoesNotChangeWhenUnrelatedOutlierAppears(t *testing.T) {
	target := []spec.Suggestion{{Cmd: "git status", Source: "history"}}
	base := SignalSet{LocalFrecency: []FrecencyEntry{{Cmd: "git status", RawScore: 500}}}
	withOutlier := base
	withOutlier.LocalFrecency = append(withOutlier.LocalFrecency, FrecencyEntry{Cmd: "unrelated", RawScore: 1_000_000})

	before := Score(target, base)[0].Breakdown.Frecency
	after := Score(target, withOutlier)[0].Breakdown.Frecency
	if before != after {
		t.Fatalf("expected stable absolute frecency, changed from %d to %d", before, after)
	}
}

func TestScore_DirectoryAffinityPrefersLocalCommand(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "deploy global", Source: "history"},
		{Cmd: "deploy local", Source: "history"},
	}
	signals := SignalSet{
		LocalFrecency: []FrecencyEntry{
			{Cmd: "deploy local", RawScore: 10},
		},
		GlobalFrecency: []FrecencyEntry{
			{Cmd: "deploy global", RawScore: 100},
			{Cmd: "deploy local", RawScore: 10},
		},
	}
	config := ScoreConfig{WeightDirectoryAffinity: 1}

	scored := ScoreWithConfig(suggestions, signals, config)
	if scored[0].Cmd != "deploy local" {
		t.Fatalf("expected local command first, got %q", scored[0].Cmd)
	}
	if scored[0].Breakdown.DirectoryAffinity != 100 {
		t.Fatalf("expected full local affinity, got %d", scored[0].Breakdown.DirectoryAffinity)
	}
}

func TestScore_ProjectHistoryOutranksGlobalOnlyHistory(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "deploy project", Source: "history"},
		{Cmd: "deploy global", Source: "history"},
	}
	signals := SignalSet{
		ProjectFrecency: []FrecencyEntry{{Cmd: "deploy project", RawScore: 10}},
		GlobalFrecency: []FrecencyEntry{
			{Cmd: "deploy project", RawScore: 10},
			{Cmd: "deploy global", RawScore: 10},
		},
	}
	config := ScoreConfig{
		WeightFrecency:          1,
		WeightDirectoryAffinity: 1,
	}

	scored := ScoreWithConfig(suggestions, signals, config)
	if scored[0].Cmd != "deploy project" {
		t.Fatalf("expected repository-scoped command first, got %q", scored[0].Cmd)
	}
	if scored[0].Breakdown.DirectoryAffinity != 70 {
		t.Fatalf("expected project affinity 70, got %d", scored[0].Breakdown.DirectoryAffinity)
	}
}

func TestScore_PrefixOverFuzzyMatch(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "make build", Source: "spec"}, // fuzzy/contains for 'bl'
		{Cmd: "block", Source: "spec"},      // prefix exact for 'bl'
	}
	signals := SignalSet{Query: "bl"}

	scored := Score(suggestions, signals)
	if len(scored) != 2 {
		t.Fatalf("expected 2 scored suggestions, got %d", len(scored))
	}
	if scored[0].Cmd != "block" {
		t.Errorf("expected prefix match 'block' to outscore fuzzy 'make build', got %s", scored[0].Cmd)
	}
}

func TestScore_LiveFilesystemPrefixOutranksHistoricalMatch(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "cd scalar-docs/", Source: "filesystem", Priority: 50},
		{Cmd: "cd scalar-legacy", Source: "history", Confidence: 55},
	}
	signals := SignalSet{
		Query: "cd scalar-",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cd scalar-legacy", RawScore: 100},
		},
		GlobalFrecency: []FrecencyEntry{
			{Cmd: "cd scalar-legacy", RawScore: 100},
		},
		TransitionEntries: []TransitionEntry{
			{NextSkeleton: "cd", Count: 10},
		},
		TransitionIsLocal: true,
	}

	scored := Score(suggestions, signals)
	if scored[0].Cmd != "cd scalar-docs/" {
		t.Fatalf("expected live directory prefix first, got %q", scored[0].Cmd)
	}
}

func TestScore_LiveFilesystemPrefixStillProtectsTypedFileArguments(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "cat config.toml", Source: "filesystem"},
		{Cmd: "cat config.old", Source: "history", Confidence: 100},
	}
	signals := SignalSet{
		Query: "cat config.t",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cat config.old", RawScore: 100},
		},
	}

	scored := Score(suggestions, signals)
	if scored[0].Cmd != "cat config.toml" {
		t.Fatalf("expected the live file prefix first, got %q", scored[0].Cmd)
	}
}

func TestScore_DirectoryFrecencyCanonicalizesTrailingSlash(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "cd alpha/", Source: "filesystem"},
		{Cmd: "cd zeta/", Source: "filesystem"},
	}
	signals := SignalSet{
		Query: "cd",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cd alpha", Count: 1, LastUsed: time.Now().Add(-time.Hour), RawScore: 1},
			{Cmd: "cd zeta", Count: 20, LastUsed: time.Now(), RawScore: 20},
		},
	}

	scored := Score(suggestions, signals)
	if scored[0].Cmd != "cd zeta/" {
		t.Fatalf("expected the frequently used directory first, got %q", scored[0].Cmd)
	}
}

func TestScore_DirectoryRankingPreference(t *testing.T) {
	now := time.Now()
	suggestions := []spec.Suggestion{
		{Cmd: "cd frequent/", Source: "filesystem"},
		{Cmd: "cd recent/", Source: "filesystem"},
	}
	signals := SignalSet{
		Query: "cd",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cd frequent", Count: 100, LastUsed: now.Add(-30 * 24 * time.Hour), RawScore: 100},
			{Cmd: "cd recent", Count: 2, LastUsed: now, RawScore: 2},
		},
	}

	frequent := ScoreWithConfig(suggestions, signals, ScoreConfig{WeightFrecency: 1, DirectoryRanking: "frequent"})
	if frequent[0].Cmd != "cd frequent/" {
		t.Fatalf("expected frequency preference to rank frequent first, got %q", frequent[0].Cmd)
	}
	recent := ScoreWithConfig(suggestions, signals, ScoreConfig{WeightFrecency: 1, DirectoryRanking: "recent"})
	if recent[0].Cmd != "cd recent/" {
		t.Fatalf("expected recency preference to rank recent first, got %q", recent[0].Cmd)
	}
}

func TestScore_BalancedDirectoryRankingPrefersSustainedRecentUseOverAncientVolume(t *testing.T) {
	now := time.Now()
	suggestions := []spec.Suggestion{
		{Cmd: "cd ancient/", Source: "filesystem"},
		{Cmd: "cd active/", Source: "filesystem"},
	}
	signals := SignalSet{
		Query: "cd",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cd ancient", Count: 1000, LastUsed: now.Add(-180 * 24 * time.Hour), RawScore: 1000},
			{Cmd: "cd active", Count: 20, LastUsed: now.Add(-35 * 24 * time.Hour), RawScore: 20},
		},
	}

	balanced := ScoreWithConfig(suggestions, signals, ScoreConfig{WeightFrecency: 1, DirectoryRanking: "balanced"})
	if balanced[0].Cmd != "cd active/" {
		t.Fatalf("expected sustained recent use to outrank ancient volume, got %q", balanced[0].Cmd)
	}
}

func TestScore_PreservesConfiguredDirectoryIndexOrderOverGenericHistorySignals(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "cd ancient/", Source: "directory-index", Priority: 91, Live: true},
		{Cmd: "cd current/", Source: "directory-index", Priority: 100, Live: true},
	}
	signals := SignalSet{
		Query: "cd",
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cd ancient", Count: 10_000, LastUsed: time.Now().Add(-180 * 24 * time.Hour)},
			{Cmd: "cd current", Count: 20, LastUsed: time.Now().Add(-24 * time.Hour)},
		},
		TransitionEntries: []TransitionEntry{{NextSkeleton: "cd ancient", Count: 10_000}},
	}

	results := ScoreWithConfig(suggestions, signals, DefaultScoreConfig)
	if len(results) != 2 || results[0].Cmd != "cd current/" {
		t.Fatalf("expected directory index policy to remain authoritative, got %+v", results)
	}
}

func TestScore_RankingContracts(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		suggestions []spec.Suggestion
		signals     SignalSet
		wantFirst   string
	}{
		{
			name:  "case-sensitive prefix outranks case-insensitive prefix",
			query: "git c",
			suggestions: []spec.Suggestion{
				{Cmd: "Git Clone", Source: "spec"},
				{Cmd: "git commit", Source: "spec"},
			},
			wantFirst: "git commit",
		},
		{
			name:  "prefix outranks contains match",
			query: "sta",
			suggestions: []spec.Suggestion{
				{Cmd: "git status", Source: "spec"},
				{Cmd: "status", Source: "spec"},
			},
			wantFirst: "status",
		},
		{
			name:  "directory-local history outranks global-only history",
			query: "deploy",
			suggestions: []spec.Suggestion{
				{Cmd: "deploy global", Source: "history"},
				{Cmd: "deploy local", Source: "history"},
			},
			signals: SignalSet{
				LocalFrecency: []FrecencyEntry{
					{Cmd: "deploy local", RawScore: 10},
				},
				GlobalFrecency: []FrecencyEntry{
					{Cmd: "deploy global", RawScore: 10},
					{Cmd: "deploy local", RawScore: 10},
				},
			},
			wantFirst: "deploy local",
		},
		{
			name:  "live filesystem prefix outranks AI",
			query: "cd scalar-",
			suggestions: []spec.Suggestion{
				{Cmd: "cd scalar-docs/", Source: "filesystem"},
				{Cmd: "cd scalar-old", Source: "ai", Confidence: 100},
			},
			wantFirst: "cd scalar-docs/",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.signals.Query = test.query
			scored := Score(test.suggestions, test.signals)
			if len(scored) == 0 || scored[0].Cmd != test.wantFirst {
				t.Fatalf("expected %q first, got %+v", test.wantFirst, scored)
			}
		})
	}
}

func TestScore_AISuggestionConfidence(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "npm run custom-script", Source: "ai", Confidence: 85},
		{Cmd: "npm help", Source: "history"},
	}
	signals := SignalSet{
		Workspace: workspace.WorkspaceInfo{HasNodeProject: true},
		Query:     "npm",
	}

	scored := Score(suggestions, signals)
	if len(scored) < 1 {
		t.Fatalf("expected scored suggestions")
	}
	if scored[0].Cmd != "npm run custom-script" {
		t.Errorf("expected high-confidence AI suggestion with context bonus at top, got %s", scored[0].Cmd)
	}
}

func TestScore_UnsortedHistorySorting(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "cmdA", Source: "history"},
		{Cmd: "cmdB", Source: "history"},
		{Cmd: "cmdC", Source: "history"},
	}
	signals := SignalSet{
		LocalFrecency: []FrecencyEntry{
			{Cmd: "cmdC", RawScore: 100.0},
			{Cmd: "cmdA", RawScore: 50.0},
			{Cmd: "cmdB", RawScore: 10.0},
		},
	}

	scored := Score(suggestions, signals)
	if len(scored) != 3 {
		t.Fatalf("expected 3 items, got %d", len(scored))
	}
	if scored[0].Cmd != "cmdC" || scored[1].Cmd != "cmdA" || scored[2].Cmd != "cmdB" {
		t.Errorf("expected cmdC > cmdA > cmdB based on frecency, got %s, %s, %s", scored[0].Cmd, scored[1].Cmd, scored[2].Cmd)
	}
}

func TestBasePriorityFor_HistoryWithConfidence(t *testing.T) {
	s1 := spec.Suggestion{Source: "history"}
	if p := basePriorityFor(s1); p != 40 {
		t.Errorf("expected default history priority 40 when confidence unset, got %d", p)
	}

	s2 := spec.Suggestion{Source: "history", Confidence: 85}
	if p := basePriorityFor(s2); p != 85 {
		t.Errorf("expected history priority 85 when confidence is 85, got %d", p)
	}

	s3 := spec.Suggestion{Source: "history", Confidence: 150}
	if p := basePriorityFor(s3); p != 100 {
		t.Errorf("expected capped history priority 100 when confidence is > 100, got %d", p)
	}
}

func TestIsSubsequence_UTF8(t *testing.T) {
	_ = isSubsequence("gố", "gõ tiếng việt")
	if !isSubsequence("việt", "tiếng việt") {
		t.Error("expected 'việt' to be subsequence of 'tiếng việt'")
	}
	if !isSubsequence("tệt", "tiếng việt") {
		t.Error("expected 'tệt' to be subsequence of 'tiếng việt'")
	}
	if isSubsequence("xyz", "tiếng việt") {
		t.Error("expected 'xyz' NOT to be subsequence of 'tiếng việt'")
	}
	if !isSubsequence("αγ", "αβγδε") {
		t.Error("expected multi-byte rune 'αγ' to be subsequence of 'αβγδε'")
	}
}

func TestScore_TransitionAndColdStartGuard(t *testing.T) {
	spec.ResetRegistry()
	spec.Register(&spec.Spec{
		Name: "git",
		Subcommands: []spec.Subcommand{
			{Name: "pull"},
			{Name: "status"},
		},
	})

	suggestions := []spec.Suggestion{
		{Cmd: "git pull --rebase origin main", Source: "spec"},
		{Cmd: "git status", Source: "spec"},
	}

	// Cold-start test: nil/empty TransitionEntries should not panic and contribute 0
	signalsCold := SignalSet{
		Query:             "git",
		TransitionEntries: nil,
	}
	scoredCold := Score(suggestions, signalsCold)
	if len(scoredCold) != 2 || scoredCold[0].Breakdown.Transition != 0 {
		t.Errorf("expected 0 transition score on cold-start without panic, got %v", scoredCold)
	}

	// Transition Local test
	signalsLocal := SignalSet{
		Query:             "git",
		TransitionEntries: []TransitionEntry{{NextSkeleton: "git pull", Count: 10}},
		TransitionIsLocal: true,
	}
	scoredLocal := Score(suggestions, signalsLocal)
	if scoredLocal[0].Breakdown.Transition != 100 {
		t.Errorf("expected transition score 100 for git pull when local, got %d", scoredLocal[0].Breakdown.Transition)
	}

	// Transition Global test (damping 70%)
	signalsGlobal := SignalSet{
		Query:             "git",
		TransitionEntries: []TransitionEntry{{NextSkeleton: "git pull", Count: 10}},
		TransitionIsLocal: false,
	}
	scoredGlobal := Score(suggestions, signalsGlobal)
	if scoredGlobal[0].Breakdown.Transition != 70 {
		t.Errorf("expected transition score 70 for git pull when global (70%% damping), got %d", scoredGlobal[0].Breakdown.Transition)
	}
}

func TestScore_ExactSequenceOutranksSkeletonFallback(t *testing.T) {
	suggestions := []spec.Suggestion{
		{Cmd: "go test ./...", Source: "history"},
		{Cmd: "go test ./internal/...", Source: "history"},
	}
	signals := SignalSet{
		ExactTransitionEntries: []ExactTransitionEntry{
			{NextCommand: "go test ./internal/...", Count: 4},
		},
		ExactTransitionIsLocal: true,
		TransitionEntries: []TransitionEntry{
			{NextSkeleton: "go test", Count: 10},
		},
		TransitionIsLocal: true,
	}
	config := ScoreConfig{WeightTransition: 1}

	scored := ScoreWithConfig(suggestions, signals, config)
	if scored[0].Cmd != "go test ./internal/..." {
		t.Fatalf("expected exact next command first, got %q", scored[0].Cmd)
	}
}

func TestScore_TieBreakingOrder(t *testing.T) {
	// All three suggestions will be constructed to have identical total scores (0),
	// but different breakdown scores to verify tie-break priority: Transition > Frecency > ContextBonus > Alphabetical.
	suggestions := []spec.Suggestion{
		{Cmd: "cmdA", Source: "test", Priority: 0},
		{Cmd: "cmdB", Source: "test", Priority: 0},
	}

	// We set weight configuration with 0 weights so total scores are identical (0), triggering tie-break logic
	zeroConfig := ScoreConfig{
		WeightBasePriority: 0,
		WeightContextBonus: 0,
		WeightFrecency:     0,
		WeightTransition:   0,
		WeightMatchQuality: 0,
	}

	signals := SignalSet{
		Query: "",
		TransitionEntries: []TransitionEntry{
			{NextSkeleton: "cmdB", Count: 10},
			{NextSkeleton: "cmdA", Count: 5},
		},
		TransitionIsLocal: true,
	}

	scored := ScoreWithConfig(suggestions, signals, zeroConfig)
	if len(scored) != 2 || scored[0].Cmd != "cmdB" {
		t.Errorf("expected cmdB to win tie-break due to higher Transition score, got %v", scored)
	}
}

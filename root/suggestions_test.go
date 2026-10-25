package root

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/scoring"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestSSHIncludeCandidatesPreserveHistoryRankAndDisplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config":      "Host vuja-alpha\nInclude parts/bravo\n",
		"parts/bravo": "Host vuja-bravo\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"ssh", "scp", "sftp", "rsync"} {
		t.Run(command, func(t *testing.T) {
			query := command + " vuja-"
			var mapped []spec.Suggestion
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				mapped = spec.LookupContextPrepared(t.Context(), query)
				if slices.ContainsFunc(mapped, func(s spec.Suggestion) bool { return s.Cmd == command+" vuja-bravo" }) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !slices.ContainsFunc(mapped, func(s spec.Suggestion) bool { return s.Cmd == command+" vuja-bravo" }) {
				t.Fatal("included host never reached production command mapping")
			}
			history := []integration.HistResult{{Cmd: command + " vuja-bravo"}}
			signals := scoring.SignalSet{Query: query} // cold/expired signal-cache fallback
			baseline := slices.DeleteFunc(slices.Clone(mapped), func(s spec.Suggestion) bool { return strings.HasSuffix(s.Cmd, "vuja-bravo") })
			before := limitScoredSuggestions(scoring.Score(collectSuggestionCandidates(query, baseline, history), signals), 1, query)
			if len(before) != 1 || before[0].Cmd != command+" vuja-bravo" {
				t.Fatalf("expected history command before discovery, got %+v", before)
			}
			after := limitScoredSuggestions(scoring.Score(collectSuggestionCandidates(query, mapped, history), signals), 1, query)
			if len(after) != 1 || after[0].Cmd != before[0].Cmd {
				t.Fatalf("discovering a previously used alias hid it: before=%+v after=%+v", before, after)
			}
			if after[0].Source != "spec" || after[0].Desc != "ssh host" {
				t.Fatalf("history merge discarded structured alias metadata: %+v", after[0])
			}
		})
	}
}

func TestCollectSuggestionCandidatesPreservesHistoryReservationForStructuredDuplicates(t *testing.T) {
	query := "deploy"
	candidates := collectSuggestionCandidates(query,
		[]spec.Suggestion{{Cmd: "deploy --help", Priority: 100}, {Cmd: "deploy production", Desc: "production target"}},
		[]integration.HistResult{{Cmd: "deploy production"}},
	)
	// Stronger specification priority can still rank first, but the existing
	// exact-prefix history reservation must survive the metadata merge.
	scored := scoring.Score(candidates, scoring.SignalSet{Query: query})
	results := limitScoredSuggestions(scored, 1, query)
	if len(results) != 1 || results[0].Cmd != "deploy production" {
		t.Fatalf("structured collision lost history reservation: %+v", results)
	}
}

func TestHistoryCollisionKeepsFunctionAndFilesystemMetadata(t *testing.T) {
	for _, source := range []string{"function", "filesystem"} {
		candidates := collectSuggestionCandidates("deploy", []spec.Suggestion{{
			ID: "provider:stable", Cmd: "deploy production", Source: source,
			Desc: "structured description", Icon: "provider-icon", Live: source == "filesystem",
		}}, []integration.HistResult{{Cmd: "deploy production"}})
		if len(candidates) != 1 || candidates[0].ID != "provider:stable" || candidates[0].Source != source || candidates[0].Desc != "structured description" || candidates[0].Icon != "provider-icon" || candidates[0].Live != (source == "filesystem") {
			t.Fatalf("history collision changed provider identity: %+v", candidates)
		}
		boostHistoryRecall(candidates)
		scored := scoring.Score(candidates, scoring.SignalSet{Query: "deploy"})
		if len(scored) != 1 || scored[0].Breakdown.BasePriority != 100 {
			t.Fatalf("history-mode recall boost was lost: %+v", scored)
		}
	}
}

func TestSuggestionPipelineDetectsChangedHistoryEvidence(t *testing.T) {
	before := []spec.Suggestion{{Cmd: "ssh prod", Source: "spec", HistoryConfidence: 90}}
	after := slices.Clone(before)
	after[0].HistoryConfidence = 65
	if sameSuggestionResults(before, after) {
		t.Fatal("changed history recall evidence was treated as the same async result")
	}
}

func TestCollectSuggestionCandidatesIncludesHistoryInDefaultMode(t *testing.T) {
	results := collectSuggestionCandidates(
		"deploy",
		[]spec.Suggestion{{Cmd: "deploy --help", Source: "spec"}},
		[]integration.HistResult{{Cmd: "deploy production"}},
	)

	var foundHistory bool
	for _, result := range results {
		if result.Cmd == "deploy production" && result.Source == "history" {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatal("expected learned history candidate in the default suggestion pool")
	}
}

func TestLimitScoredSuggestionsKeepsExactPrefixHistoryVisible(t *testing.T) {
	scored := []scoring.ScoredSuggestion{
		{Suggestion: spec.Suggestion{Cmd: "deploy --help", Source: "spec"}, Score: 100},
		{Suggestion: spec.Suggestion{Cmd: "deploy --version", Source: "spec"}, Score: 90},
		{Suggestion: spec.Suggestion{Cmd: "deploy production", Source: "history"}, Score: 80},
	}

	results := limitScoredSuggestions(scored, 2, "deploy")

	if len(results) != 2 {
		t.Fatalf("expected two displayed suggestions, got %+v", results)
	}
	if results[1].Cmd != "deploy production" || results[1].Source != "history" {
		t.Fatalf("expected an exact-prefix history candidate to survive the specification cap, got %+v", results)
	}
}

func TestLimitScoredSuggestionsDoesNotReserveFuzzyHistory(t *testing.T) {
	scored := []scoring.ScoredSuggestion{
		{Suggestion: spec.Suggestion{Cmd: "deploy --help", Source: "spec"}, Score: 100},
		{Suggestion: spec.Suggestion{Cmd: "deploy --version", Source: "spec"}, Score: 90},
		{Suggestion: spec.Suggestion{Cmd: "run deploy production", Source: "history"}, Score: 80},
	}

	results := limitScoredSuggestions(scored, 2, "deploy")

	if results[1].Cmd != "deploy --version" {
		t.Fatalf("expected a non-prefix history match to follow normal ranking, got %+v", results)
	}
}

func TestCollectSuggestionCandidatesKeepsStructuredMetadataForDuplicates(t *testing.T) {
	results := collectSuggestionCandidates(
		"deploy",
		[]spec.Suggestion{{Cmd: "deploy production", Desc: "Deploy the production environment", Source: "spec"}},
		[]integration.HistResult{{Cmd: "deploy production"}},
	)

	if len(results) != 1 {
		t.Fatalf("expected one deduplicated candidate, got %v", results)
	}
	if results[0].Source != "spec" || results[0].Desc != "Deploy the production environment" {
		t.Fatalf("expected structured metadata to be preserved, got %+v", results[0])
	}
}

func TestDedupeSuggestionCandidatesMergesLiveDirectoryWithIndexedRank(t *testing.T) {
	results := dedupeSuggestionCandidates("cd", []spec.Suggestion{
		{Cmd: "cd api/", Desc: "directory", Source: "filesystem", Priority: 50},
		{Cmd: "cd api", Desc: "visited directory", Source: "directory-index", Priority: 91},
	})

	if len(results) != 1 {
		t.Fatalf("expected one canonical directory candidate, got %v", results)
	}
	if results[0].Cmd != "cd api/" || results[0].Source != "directory-index" {
		t.Fatalf("expected live spelling and indexed provenance, got %+v", results[0])
	}
	if results[0].Priority != 91 {
		t.Fatalf("expected indexed rank to survive deduplication, got %d", results[0].Priority)
	}
	if !results[0].Live {
		t.Fatal("expected merged directory candidate to retain live filesystem evidence")
	}
}

func TestMergeResults(t *testing.T) {

	t.Run("Dedup exact match", func(t *testing.T) {
		// Mock history items that might conflict with specs
		res := MergeResults("git", "spec")
		seen := make(map[string]bool)
		for _, r := range res {
			if seen[r.Cmd] {
				t.Errorf("Duplicate suggestion found: %q", r.Cmd)
			}
			seen[r.Cmd] = true
		}
	})

	t.Run("Limit 100", func(t *testing.T) {
		res := MergeResults("a", "history")
		if len(res) > 100 {
			t.Errorf("Expected max 100 suggestions, got %d", len(res))
		}
	})

	t.Run("AI Suggestion Promotion", func(t *testing.T) {
		aiSugg := &spec.Suggestion{
			Cmd:        "git commit -m \"fix(auth): login bug\"",
			Desc:       "AI suggestion",
			Source:     "ai",
			Confidence: 85,
		}
		SetCurrentAISuggestion(aiSugg)
		defer SetCurrentAISuggestion(nil)

		res := MergeResults("git c", "history")
		if len(res) == 0 {
			t.Fatalf("Expected suggestions, got 0")
		}
		if res[0].Cmd != aiSugg.Cmd {
			t.Errorf("Expected AI suggestion at index 0, got %q (confidence %d)", res[0].Cmd, res[0].Confidence)
		}
		if res[0].Source != "ai" {
			t.Errorf("Expected promoted source 'ai', got %q", res[0].Source)
		}
	})
}

func TestPrevRecordedCommandState(t *testing.T) {
	origRegistry := spec.Registry
	t.Cleanup(func() {
		spec.Registry = origRegistry
	})

	spec.ResetRegistry()
	spec.Register(&spec.Spec{
		Name: "git",
		Subcommands: []spec.Subcommand{
			{Name: "checkout"},
		},
	})

	setPrevRecordedInfo("git checkout feature-abc", "/repo/dir")
	skel, cwd := getPrevRecordedInfo()
	if skel != "git checkout" || cwd != "/repo/dir" {
		t.Errorf("expected skel='git checkout' and cwd='/repo/dir', got skel=%q, cwd=%q", skel, cwd)
	}
	if gotSkel := getPrevSkeleton(); gotSkel != "git checkout" {
		t.Errorf("expected getPrevSkeleton()='git checkout', got %q", gotSkel)
	}
	command, commandSkeleton := getPrevCommandSignals()
	if command != "git checkout feature-abc" || commandSkeleton != "git checkout" {
		t.Errorf("expected atomic full and skeleton signals, got command=%q, skeleton=%q", command, commandSkeleton)
	}

	setPrevRecordedInfo("", "")
	if gotSkel := getPrevSkeleton(); gotSkel != "" {
		t.Errorf("expected empty getPrevSkeleton() after reset, got %q", gotSkel)
	}
}

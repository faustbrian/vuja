package spec

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseCobraOutput_ValidCobra(t *testing.T) {
	raw := "install\tinstall a chart\nupgrade\tupgrade a release\nstatus\tget release status\n:4\n"
	results := parseCobraOutput(raw, "helm")
	if len(results) != 3 {
		t.Fatalf("expected 3 suggestions, got %d", len(results))
	}
	if results[0].Cmd != "helm install" {
		t.Errorf("expected 'helm install', got %q", results[0].Cmd)
	}
	if results[0].Desc != "install a chart" {
		t.Errorf("expected desc 'install a chart', got %q", results[0].Desc)
	}
	if results[0].Source != "spec-inferred" {
		t.Errorf("expected source 'spec-inferred', got %q", results[0].Source)
	}
	if results[0].Priority != 30 {
		t.Errorf("expected priority 30, got %d", results[0].Priority)
	}
}

func TestQueryCobraCompleteCachesCompletionOutput(t *testing.T) {
	ResetCobraCache()
	t.Cleanup(ResetCobraCache)

	originalRunner := runCobraCompletion
	var calls atomic.Int32
	runCobraCompletion = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		calls.Add(1)
		return []byte("status\tshow status\n:0\n"), nil
	}
	t.Cleanup(func() { runCobraCompletion = originalRunner })

	if got := QueryCobraComplete("test-cobra", nil, ""); len(got) != 0 {
		t.Fatalf("expected cold completion not to block, got %v", got)
	}
	got := waitForCobraSuggestions(t, func() []Suggestion { return QueryCobraComplete("test-cobra", nil, "") })
	if len(got) != 1 || got[0].Cmd != "test-cobra status" {
		t.Fatalf("expected cached Cobra completion, got %v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one Cobra subprocess call, got %d", calls.Load())
	}
}

func TestQueryCobraCompleteHonorsCancellation(t *testing.T) {
	ResetCobraCache()
	original := runCobraCompletion
	t.Cleanup(func() { runCobraCompletion = original })
	started := make(chan struct{})
	done := make(chan struct{})
	runCobraCompletion = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(done)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	if got := QueryCobraCompleteContext(ctx, "tool", nil, ""); len(got) != 0 {
		t.Fatalf("expected cold completion not to block, got %+v", got)
	}
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dynamic completion did not stop after cancellation")
	}
}

func TestLookupDoesNotExecuteUndeclaredDynamicCompletion(t *testing.T) {
	ResetRegistry()
	ResetCobraCache()
	t.Cleanup(ResetRegistry)
	var calls atomic.Int32
	original := runCobraCompletion
	t.Cleanup(func() { runCobraCompletion = original })
	runCobraCompletion = func(context.Context, string, []string) ([]byte, error) {
		calls.Add(1)
		return []byte("value\tdescription\n:0\n"), nil
	}

	if got := Lookup("unregistered value"); len(got) != 0 {
		t.Fatalf("expected no undeclared dynamic results, got %+v", got)
	}
	Register(&Spec{Name: "registered", DynamicCompletion: true})
	if got := Lookup("registered value"); len(got) != 0 {
		t.Fatalf("expected declared provider to populate asynchronously, got %+v", got)
	}
	if got := waitForCobraSuggestions(t, func() []Suggestion { return Lookup("registered value") }); len(got) == 0 {
		t.Fatal("expected explicitly declared dynamic completion results")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly one declared provider invocation, got %d", calls.Load())
	}
}

func TestCobraCompletionCacheIsBounded(t *testing.T) {
	ResetCobraCache()
	original := runCobraCompletion
	t.Cleanup(func() { runCobraCompletion = original })
	runCobraCompletion = func(context.Context, string, []string) ([]byte, error) {
		return []byte("value\tdescription\n:0\n"), nil
	}
	cobraCacheMu.Lock()
	for index := 0; index < cobraCacheLimit+20; index++ {
		cobraCache[strings.Repeat("x", index+1)] = cobraCacheEntry{expires: time.Now().Add(time.Hour), lastUsed: time.Unix(int64(index+1), 0)}
	}
	pruneCobraCacheLocked()
	size := len(cobraCache)
	cobraCacheMu.Unlock()
	if size > cobraCacheLimit {
		t.Fatalf("expected at most %d cached completions, got %d", cobraCacheLimit, size)
	}
}

func waitForCobraSuggestions(t *testing.T, query func() []Suggestion) []Suggestion {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if suggestions := query(); len(suggestions) > 0 {
			return suggestions
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func TestParseCobraOutput_ErrorDirective(t *testing.T) {
	// directive bit 1 = ShellCompDirectiveError — not a Cobra CLI
	raw := "something\n:1\n"
	results := parseCobraOutput(raw, "mycmd")
	if results != nil {
		t.Errorf("expected nil for error directive, got %v", results)
	}
}

func TestParseCobraOutput_NoDirectiveLine(t *testing.T) {
	raw := "just some --help output\nno directive here\n"
	results := parseCobraOutput(raw, "mycmd")
	if results != nil {
		t.Errorf("expected nil for non-Cobra output, got %v", results)
	}
}

func TestParseCobraOutput_PartialFilter(t *testing.T) {
	raw := "get\tget resources\ndelete\tdelete resources\ndescribe\tdescribe resources\n:4\n"
	// parseCobraOutput returns all candidates; filterByPartial handles narrowing
	results := parseCobraOutput(raw, "kubectl")
	filtered := filterByPartial(results, "de")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 results matching 'de', got %d: %v", len(filtered), filtered)
	}
}

func TestQueryCobraComplete_PathTraversalBlocked(t *testing.T) {
	result := QueryCobraComplete("./malicious.sh", nil, "")
	if result != nil {
		t.Errorf("expected nil for path traversal input, got %v", result)
	}
	result = QueryCobraComplete("/usr/bin/env", nil, "")
	if result != nil {
		t.Errorf("expected nil for absolute path input, got %v", result)
	}
}

func TestQueryCobraComplete_NonCobraBinary(t *testing.T) {
	t.Cleanup(ResetCobraCache)
	// 'ls' is not Cobra-based, should return nil gracefully
	result := QueryCobraComplete("ls", nil, "")
	if result != nil {
		t.Fatalf("expected nil for non-Cobra binary 'ls', got %v", result)
	}
}

func TestFilterByPartial(t *testing.T) {
	suggestions := []Suggestion{
		{Cmd: "helm install"},
		{Cmd: "helm upgrade"},
		{Cmd: "helm status"},
	}
	filtered := filterByPartial(suggestions, "up")
	if len(filtered) != 1 || filtered[0].Cmd != "helm upgrade" {
		t.Errorf("expected [helm upgrade], got %v", filtered)
	}
}

func TestFilterByPartial_EmptyPartial(t *testing.T) {
	suggestions := []Suggestion{{Cmd: "a"}, {Cmd: "b"}}
	filtered := filterByPartial(suggestions, "")
	if len(filtered) != 2 {
		t.Errorf("expected all suggestions for empty partial, got %d", len(filtered))
	}
}

func TestBuildCobraCacheKey(t *testing.T) {
	key1 := buildCobraCacheKey("gh", []string{"repo", "foo bar"}, "baz")
	key2 := buildCobraCacheKey("gh", []string{"repo", "foo", "bar"}, "baz")
	key3 := buildCobraCacheKey("gh", []string{"repo", "foo bar"}, "other")

	if key1 == key2 {
		t.Errorf("expected quoted argument key1 and key2 to be distinct, but were equal")
	}
	if key1 != key3 {
		t.Errorf("expected partials to share one command-context cache key")
	}
}

func TestLookup_CobraRealBinary(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)
	Register(&Spec{Name: "gh", DynamicCompletion: true})
	_ = Lookup("gh repo ")
	results := waitForCobraSuggestions(t, func() []Suggestion { return Lookup("gh repo ") })
	if len(results) == 0 {
		t.Skip("gh binary not available or output no completions")
	}
	t.Logf("Got %d completions for 'gh repo ':", len(results))
	for _, r := range results {
		t.Logf("  - Cmd: %-25s | Source: %-15s | Priority: %d | Desc: %s", r.Cmd, r.Source, r.Priority, r.Desc)
	}
}

func TestLookup_CobraKubectl(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)
	Register(&Spec{Name: "kubectl", DynamicCompletion: true})
	_ = Lookup("kubectl get ")
	results := waitForCobraSuggestions(t, func() []Suggestion { return Lookup("kubectl get ") })
	if len(results) == 0 {
		t.Skip("kubectl binary not available or output no completions")
	}
	t.Logf("Got %d completions for 'kubectl get ':", len(results))
	for i, r := range results {
		if i >= 10 {
			t.Logf("  ... and %d more", len(results)-10)
			break
		}
		t.Logf("  - Cmd: %-30s | Source: %-15s | Priority: %d | Desc: %s", r.Cmd, r.Source, r.Priority, r.Desc)
	}
}

func TestLookup_CobraGolangciLint(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)
	Register(&Spec{Name: "golangci-lint", DynamicCompletion: true})
	_ = Lookup("golangci-lint ")
	results := waitForCobraSuggestions(t, func() []Suggestion { return Lookup("golangci-lint ") })
	if len(results) == 0 {
		t.Skip("golangci-lint binary not available or output no completions")
	}
	t.Logf("Got %d completions for 'golangci-lint ':", len(results))
	for _, r := range results {
		t.Logf("  - Cmd: %-30s | Source: %-15s | Priority: %d | Desc: %s", r.Cmd, r.Source, r.Priority, r.Desc)
	}
}

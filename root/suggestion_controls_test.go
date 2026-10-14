package root

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/spec"
)

func TestSuggestionControlsDoNotActOnGhostText(t *testing.T) {
	o := integration.NewOverlay(false)
	o.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git status"}})
	queued := false
	consumed := handleSuggestionControl([]byte("\x1b[15~"), 0, newInputKeybindings(config.DefaultConfig().Keybindings), o,
		func(suggestionPreferenceRequest) bool { queued = true; return true })
	if consumed != 5 || queued || o.GetCurrentCmd() != "" || o.GetTypedQuery() != "git" {
		t.Fatalf("ghost text gained preference/execution authority: consumed=%d queued=%v", consumed, queued)
	}
}

func TestSuggestionControlsCaptureOnlyTheSelectedCommand(t *testing.T) {
	o := integration.NewOverlay(false)
	o.SetQueryAndItems("git", []spec.Suggestion{{ID: "status", Cmd: "git status"}, {ID: "diff", Cmd: "git diff"}})
	o.FocusFirst()
	o.MoveCursor("down")
	var requested suggestionPreferenceRequest
	consumed := handleSuggestionControl([]byte("\x1b[17~"), 0, newInputKeybindings(config.DefaultConfig().Keybindings), o,
		func(request suggestionPreferenceRequest) bool { requested = request; return true })
	if consumed != 5 || requested.command != "git diff" || requested.action != inputSuggestionBlock || o.GetTypedQuery() != "git" || o.GetCurrentCmd() != "git diff" {
		t.Fatalf("row action changed input or captured another target: %+v", requested)
	}
}

func TestSuggestionPreferenceWorkerKeepsInputProgressIndependentOfDisk(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	w := newSuggestionPreferenceWorkerWithWriter(func(ctx context.Context, request suggestionPreferenceRequest) (config.SuggestionsConfig, error) {
		close(started)
		select {
		case <-release:
			return config.SuggestionsConfig{Pins: []string{request.command}}, nil
		case <-ctx.Done():
			return config.SuggestionsConfig{}, ctx.Err()
		}
	})
	t.Cleanup(w.close)
	if !w.enqueue(suggestionPreferenceRequest{inputSuggestionPin, "git status"}) {
		t.Fatal("first preference was not queued")
	}
	<-started
	if w.enqueue(suggestionPreferenceRequest{inputSuggestionPin, "git diff"}) {
		t.Fatal("a blocked writer accepted another unbounded preference")
	}
	// This is a synchronization-barrier assertion, not a latency threshold.
	o := integration.NewOverlay(false)
	o.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git status"}})
	if got := o.FocusFirst(); got != "git status" || o.GetTypedQuery() != "git" {
		t.Fatal("pending disk work prevented input/selection progress")
	}
	close(release)
	select {
	case result := <-w.results:
		if result.err != nil || !slices.Equal(result.preferences.Pins, []string{"git status"}) {
			t.Fatalf("saved preference result is incorrect: %v", result.err)
		}
		w.acknowledge()
	case <-time.After(3 * time.Second):
		t.Fatal("preference worker did not finish after disk release")
	}
}

func TestSuggestionPreferencesPersistLiteralBlocksAndPreserveConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := config.DefaultConfig()
	cfg.UI.Chatbox.Prompt = "custom> "
	cfg.Suggestions.Pins = []string{"git status"}
	cfg.AI.Providers = map[string]config.ProviderConfig{"example": {APIKeyEnv: "VUJA_TEST_PREFERENCE_KEY"}}
	content, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WritePrivateFile(path, content); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VUJA_TEST_PREFERENCE_KEY", "not-to-be-written")
	command := "printf '*'"
	preferences, err := saveSuggestionPreference(context.Background(), path, suggestionPreferenceRequest{inputSuggestionBlock, command})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.BlockedBy(preferences.Blocks, preferences.ExactBlocks, command) || policy.BlockedBy(preferences.Blocks, preferences.ExactBlocks, "printf 'different'") {
		t.Fatal("UI block was interpreted as a wildcard rather than the selected command")
	}
	if policy.BlockedBy(preferences.Blocks, preferences.ExactBlocks, `printf '\*'`) {
		t.Fatal("an exact UI block also hid the different escaped-text command")
	}
	_, reloaded, err := readSetupConfig(path)
	if err != nil || reloaded == nil || reloaded.UI.Chatbox.Prompt != "custom> " || reloaded.AI.Providers["example"].APIKeyEnv != "VUJA_TEST_PREFERENCE_KEY" || !slices.Equal(reloaded.Suggestions.Pins, []string{"git status"}) {
		t.Fatalf("saving a row preference altered unrelated configuration: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "not-to-be-written") {
		t.Fatal("runtime credential resolution leaked into saved configuration")
	}
	backup, err := os.ReadFile(path + ".suggestions.bak")
	if err != nil || string(backup) != string(content) {
		t.Fatal("original configuration was not retained in the predecessor backup")
	}
	for _, file := range []string{path, path + ".suggestions.bak", path + ".suggestions.lock"} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("preference artifact is not private: %v", err)
		}
	}
}

func TestSuggestionPreferencesBlockOnlyTheLiteralSelectedCommand(t *testing.T) {
	for _, test := range []struct{ command, different string }{
		{`printf '*'`, `printf '\*'`},
		{`printf '?'`, `printf '\?'`},
		{`printf '[ab]'`, `printf '\[ab\]'`},
		{`printf '\path'`, `printf '\\path'`},
	} {
		t.Run(test.command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			_, err := saveSuggestionPreference(context.Background(), path, suggestionPreferenceRequest{inputSuggestionBlock, test.command})
			if err != nil {
				t.Fatal(err)
			}
			_, cfg, err := readSetupConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if !policy.BlockedBy(cfg.Suggestions.Blocks, cfg.Suggestions.ExactBlocks, test.command) || policy.BlockedBy(cfg.Suggestions.Blocks, cfg.Suggestions.ExactBlocks, test.different) {
				t.Fatal("reloaded exact block failed to distinguish literal commands")
			}
		})
	}
	if !policy.BlockedBy([]string{"git *", "literal ["}, nil, "git status") || !policy.BlockedBy([]string{"literal ["}, nil, "literal [") {
		t.Fatal("manual wildcard or malformed literal block compatibility was lost")
	}
}

func TestSuggestionPreferencesPreserveExistingManualBlockSemantics(t *testing.T) {
	for _, command := range []string{"echo [ab]", "echo a", "echo b"} {
		if !policy.BlockedBy([]string{"echo [ab]"}, nil, command) {
			t.Fatalf("existing manual exclusion stopped blocking %q", command)
		}
	}
}

func TestPreferencePublicationKeepsOldSnapshotsAndRuntimeOverrides(t *testing.T) {
	before := config.Get()
	cfg := config.DefaultConfig()
	cfg.Core.Debug = true
	cfg.Suggestions.Pins = []string{"old"}
	config.Init(cfg)
	t.Cleanup(func() { config.Init(before) })
	pins, blocks := []string{"new"}, []string{"blocked"}
	config.PublishSuggestionPreferences(config.SuggestionsConfig{Pins: pins, Blocks: blocks, ExactBlocks: []string{"literal*"}})
	pins[0], blocks[0] = "mutated", "mutated"
	if !slices.Equal(cfg.Suggestions.Pins, []string{"old"}) || !slices.Equal(config.Get().Suggestions.Pins, []string{"new"}) || !config.Get().Core.Debug {
		t.Fatal("publishing preferences mutated an old snapshot, aliased input, or lost runtime overrides")
	}
	if !policy.Blocked("literal*") || policy.Blocked("literal-other") {
		t.Fatal("published exact blocks did not reach the actual policy consumer")
	}
}

func TestPreferenceChangesDiscardCachedAndInFlightSuggestions(t *testing.T) {
	before := config.Get()
	t.Cleanup(func() { config.Init(before) })
	p := newSuggestionPipeline(4)
	request := suggestionRequest{query: "git", mode: "spec", cwd: "/example"}
	generation := p.begin(request)
	p.seed(generation, request, []spec.Suggestion{{Cmd: "git status"}})
	p.publishPreferences(before.Suggestions)
	if _, ok := p.immediate(request); ok || p.commit(generation, request, []spec.Suggestion{{Cmd: "git status"}}) {
		t.Fatal("an old preference generation survived cache clearing")
	}
}

func TestSuggestionPreferencesTogglePinsAndRejectUnsafeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, expectedPins := range [][]string{{"git status"}, nil} {
		_, err := saveSuggestionPreference(context.Background(), path, suggestionPreferenceRequest{inputSuggestionPin, "git status"})
		if err != nil {
			t.Fatal(err)
		}
		_, cfg, err := readSetupConfig(path)
		if err != nil || !slices.Equal(cfg.Suggestions.Pins, expectedPins) {
			t.Fatalf("pin/unpin did not survive reload: %v", err)
		}
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := saveSuggestionPreference(ctx, path, suggestionPreferenceRequest{inputSuggestionBlock, "git status"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preference action did not stop: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(original) {
		t.Fatal("cancelled action altered the original config")
	}
	link := filepath.Join(filepath.Dir(path), "config-link.toml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := saveSuggestionPreference(context.Background(), link, suggestionPreferenceRequest{inputSuggestionPin, "git diff"}); err == nil {
		t.Fatal("preference writer followed a non-regular config file")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(original) {
		t.Fatal("rejected config file altered its target")
	}
}

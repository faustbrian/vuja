package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigAndState(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Core.Version != CurrentVersion {
		t.Errorf("expected version %d, got %d", CurrentVersion, cfg.Core.Version)
	}
	if cfg.UI.MaxSuggestions != 100 {
		t.Errorf("expected suggestions 100, got %d", cfg.UI.MaxSuggestions)
	}
	if cfg.AI.Enabled {
		t.Errorf("expected AI to be disabled by default")
	}
	if cfg.AI.Provider != "" {
		t.Errorf("expected default provider to be empty, got %q", cfg.AI.Provider)
	}
	if cfg.AI.Providers != nil {
		t.Errorf("expected default providers map to be nil, got %v", cfg.AI.Providers)
	}
	if cfg.Keybindings.HistorySearch != "ctrl+r" {
		t.Errorf("expected history search to default to ctrl+r, got %q", cfg.Keybindings.HistorySearch)
	}
	if cfg.Keybindings.HistorySuccessOnly != "ctrl+s" {
		t.Errorf("expected successful-history filter to default to ctrl+s, got %q", cfg.Keybindings.HistorySuccessOnly)
	}
	if cfg.Keybindings.Accept != "tab" || cfg.Keybindings.ToggleOverlay != "shift+tab" {
		t.Errorf("unexpected default overlay keybindings: %+v", cfg.Keybindings)
	}
	if got := cfg.Keybindings.AcceptToken; len(got) != 3 ||
		got[0] != "alt+right" || got[1] != "ctrl+right" || got[2] != "meta+right" {
		t.Errorf("unexpected default token-accept keybindings: %v", got)
	}
	if cfg.UI.Chatbox.Prompt != "› " || cfg.UI.Chatbox.Separator != " · " {
		t.Fatalf("unexpected default chatbox prompt: %+v", cfg.UI.Chatbox)
	}
	if cfg.UI.Chatbox.Scrollback != "output" {
		t.Fatalf("expected completed commands to default to output-only scrollback, got %q", cfg.UI.Chatbox.Scrollback)
	}
	if cfg.UI.Chatbox.PathColorMode != "hierarchy" || cfg.UI.Chatbox.PathMaxSegments != 6 {
		t.Fatalf("unexpected default path rendering: %+v", cfg.UI.Chatbox)
	}
	if cfg.UI.Chatbox.HistorySpacing != 1 {
		t.Fatalf("expected one row between completed executions, got %d", cfg.UI.Chatbox.HistorySpacing)
	}
	wantSegments := []string{
		"directory", "package", "versions", "session", "git-branch", "git-status", "git-added", "git-deleted",
		"git-stash", "git-lines", "environment", "version-mismatch", "contexts", "stale",
		"jobs", "duration", "exit", "cpu", "memory",
	}
	if strings.Join(cfg.UI.Chatbox.Segments(), ",") != strings.Join(wantSegments, ",") {
		t.Fatalf("unexpected default chatbox regions: %+v", cfg.UI.Chatbox)
	}
	if strings.Join(cfg.UI.Chatbox.TitleLeft, ",") != "directory" || strings.Join(cfg.UI.Chatbox.TitleRight, ",") != "package,versions" ||
		strings.Join(cfg.UI.Chatbox.StatusLeft, ",") != "session,git-branch,git-status,git-added,git-deleted,git-stash,git-lines,environment,version-mismatch,contexts,stale" ||
		strings.Join(cfg.UI.Chatbox.StatusRight, ",") != "jobs,duration,exit,cpu,memory" {
		t.Fatalf("unexpected default chatbox placement: %+v", cfg.UI.Chatbox)
	}
	if time.Duration(cfg.UI.Chatbox.RefreshInterval) != time.Second {
		t.Fatalf("unexpected chatbox refresh interval: %s", time.Duration(cfg.UI.Chatbox.RefreshInterval))
	}
	if cfg.UI.Chatbox.MetricHysteresis != 2 {
		t.Fatalf("unexpected metric hysteresis: %v", cfg.UI.Chatbox.MetricHysteresis)
	}
	if cfg.UI.Colors.Night.SurfaceBackground != "#242528" || cfg.UI.Colors.Night.CompletedSurfaceBackground != "#17191d" || cfg.UI.Colors.Night.StatusBackground != "#080a0d" {
		t.Fatalf("unexpected default night chatbox colors: %+v", cfg.UI.Colors.Night)
	}
	if cfg.UI.Chatbox.Colors.PHP != "#777bb4" || cfg.UI.Chatbox.Colors.Laravel != "#ff2d20" {
		t.Fatalf("unexpected default brand colors: %+v", cfg.UI.Chatbox.Colors)
	}
	if cfg.UI.Chatbox.Colors.GitModified != "#f3c969" || cfg.UI.Chatbox.Colors.DurationSlow != "#ff7b72" || cfg.UI.Chatbox.Colors.LoadCritical != "#ff7b72" {
		t.Fatalf("unexpected default semantic colors: %+v", cfg.UI.Chatbox.Colors)
	}
	if cfg.UI.Chatbox.Colors.ExitNeutral != "#747579" {
		t.Fatalf("unexpected neutral exit default: %+v", cfg.UI.Chatbox.Colors)
	}

	// test manual provider registration
	cfg.AI.Provider = "custom"
	cfg.AI.Providers = map[string]ProviderConfig{
		"custom": {
			InheritedFrom: "openai",
			Endpoint:      "https://custom-api.com/v1",
			APIKey:        "test-key",
			Model:         "test-model",
			TimeoutMS:     1000,
		},
	}
	p, ok := cfg.AI.GetActiveProvider()
	if !ok {
		t.Fatalf("expected custom provider to exist")
	}
	if p.InheritedFrom != "openai" {
		t.Errorf("expected inherited_from openai, got %q", p.InheritedFrom)
	}
	if p.GetAPIKey() != "test-key" {
		t.Errorf("expected api key test-key, got %q", p.GetAPIKey())
	}
	if cfg.AI.SuggestOnEmpty.DebounceMS != 800 {
		t.Errorf("expected debounce 800, got %d", cfg.AI.SuggestOnEmpty.DebounceMS)
	}
	if cfg.AI.SuggestOnEmpty.MinIntervalMS != 5000 {
		t.Errorf("expected min interval 5000, got %d", cfg.AI.SuggestOnEmpty.MinIntervalMS)
	}

	state := DefaultState()
	if state.LastMode != "spec" {
		t.Errorf("expected last mode spec, got %q", state.LastMode)
	}
}

func TestLoadCustomChatboxConfiguration(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox]
prompt = "> "
separator = " | "
scrollback = "snapshot"
path-color-mode = "single"
path-max-segments = 4
history-spacing = 2
status = ["directory", "exit"]
refresh-interval = "2s"
collapse-versions = false

[ui.chatbox.colors]
php = "#112233"

[ui.colors.night]
surface-background = "#223344"
completed-surface-background = "#2a3b4c"
status-background = "#334455"
status-text = "#445566"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Chatbox.Prompt != "> " || cfg.UI.Chatbox.Separator != " | " ||
		strings.Join(cfg.UI.Chatbox.StatusLeft, ",") != "directory" || strings.Join(cfg.UI.Chatbox.StatusRight, ",") != "exit" {
		t.Fatalf("unexpected custom chatbox config: %+v", cfg.UI.Chatbox)
	}
	if cfg.UI.Chatbox.Scrollback != "snapshot" {
		t.Fatalf("expected custom snapshot scrollback, got %q", cfg.UI.Chatbox.Scrollback)
	}
	if cfg.UI.Chatbox.PathColorMode != "single" || cfg.UI.Chatbox.PathMaxSegments != 4 ||
		cfg.UI.Chatbox.HistorySpacing != 2 {
		t.Fatalf("unexpected custom path or history config: %+v", cfg.UI.Chatbox)
	}
	if time.Duration(cfg.UI.Chatbox.RefreshInterval) != 2*time.Second || cfg.UI.Chatbox.CollapseVersions {
		t.Fatalf("unexpected custom chatbox timing: %+v", cfg.UI.Chatbox)
	}
	if cfg.UI.Chatbox.Colors.PHP != "#112233" || cfg.UI.Chatbox.Colors.Go == "" {
		t.Fatalf("expected custom PHP and default Go colors, got %+v", cfg.UI.Chatbox.Colors)
	}
	if cfg.UI.Colors.Night.SurfaceBackground != "#223344" || cfg.UI.Colors.Night.CompletedSurfaceBackground != "#2a3b4c" || cfg.UI.Colors.Night.StatusBackground != "#334455" || cfg.UI.Colors.Night.StatusText != "#445566" {
		t.Fatalf("unexpected custom surface colors: %+v", cfg.UI.Colors.Night)
	}
}

func TestLoadCustomChatboxRegions(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox]
title-left = ["directory"]
title-center = ["git-branch"]
title-right = ["versions"]
status-left = ["git-status"]
status-center = ["duration"]
status-right = ["exit", "cpu", "memory"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.UI.Chatbox.TitleCenter, ",") != "git-branch" ||
		strings.Join(cfg.UI.Chatbox.StatusCenter, ",") != "duration" ||
		strings.Join(cfg.UI.Chatbox.StatusRight, ",") != "exit,cpu,memory" {
		t.Fatalf("unexpected custom chatbox regions: %+v", cfg.UI.Chatbox)
	}
}

func TestLoadMigratesLegacyDefaultStatusToTitleAndStatusRegions(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox]
status = ["directory", "git-branch", "git-status", "git-added", "git-deleted", "duration", "exit", "versions", "cpu", "memory"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.UI.Chatbox.TitleLeft, ",") != "directory" || strings.Join(cfg.UI.Chatbox.TitleRight, ",") != "package,versions" ||
		strings.Join(cfg.UI.Chatbox.StatusRight, ",") != "jobs,duration,exit,cpu,memory" {
		t.Fatalf("expected legacy defaults to adopt the regional layout, got %+v", cfg.UI.Chatbox)
	}
}

func TestLoadMigratesPreviousGeneratedRegionalDefaults(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox]
title-left = ["directory"]
title-center = []
title-right = ["versions"]
status-left = ["git-branch", "git-status", "git-added", "git-deleted"]
status-center = []
status-right = ["duration", "exit", "cpu", "memory"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.UI.Chatbox.TitleRight, ",") != "package,versions" ||
		!strings.Contains(strings.Join(cfg.UI.Chatbox.StatusLeft, ","), "version-mismatch") ||
		strings.Join(cfg.UI.Chatbox.StatusRight, ",") != "jobs,duration,exit,cpu,memory" {
		t.Fatalf("expected previous generated defaults to adopt new finite providers, got %+v", cfg.UI.Chatbox)
	}
}

func TestLoadAllowsTitleAndStatusBarsToBeDisabled(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox]
title-left = []
title-center = []
title-right = []
status-left = []
status-center = []
status-right = []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if segments := cfg.UI.Chatbox.Segments(); len(segments) != 0 {
		t.Fatalf("expected title and status bars to be disabled, got %v", segments)
	}
}

func TestLoadPreservesLegacyChatboxColorOverrides(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.chatbox.colors]
git-status = "#111111"
duration = "#222222"
cpu = "#333333"
memory = "#444444"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	colors := cfg.UI.Chatbox.Colors
	if colors.GitModified != "#111111" || colors.GitConflicts != "#111111" {
		t.Fatalf("expected legacy Git color to cover semantic states, got %+v", colors)
	}
	if colors.DurationFast != "#222222" || colors.DurationSlow != "#222222" {
		t.Fatalf("expected legacy duration color to cover thresholds, got %+v", colors)
	}
	if !colors.UseLegacyCPU || !colors.UseLegacyMemory {
		t.Fatalf("expected legacy metric colors to remain active, got %+v", colors)
	}
}

func TestLoadRejectsUnsafeChatboxConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "unknown segment", content: `[ui.chatbox]
status = ["directory", "custom"]`, want: "unknown status segment"},
		{name: "duplicate segment", content: `[ui.chatbox]
status = ["exit", "exit"]`, want: "duplicate status segment"},
		{name: "duplicate across regions", content: `[ui.chatbox]
title-left = ["directory"]
status-left = ["directory"]`, want: "duplicate status segment"},
		{name: "legacy and regions", content: `[ui.chatbox]
status = ["directory"]
title-left = ["directory"]`, want: "cannot be combined"},
		{name: "ANSI prompt", content: "[ui.chatbox]\nprompt = \"\\u001b[31m> \"", want: "prompt"},
		{name: "empty prompt", content: "[ui.chatbox]\nprompt = \"   \"", want: "prompt"},
		{name: "command substitution", content: `[ui.chatbox]
prompt = "$(whoami) "`, want: "prompt"},
		{name: "Zsh prompt expansion", content: `[ui.chatbox]
prompt = "%n "`, want: "prompt"},
		{name: "parameter expansion", content: `[ui.chatbox]
prompt = "${USER} "`, want: "prompt"},
		{name: "unsafe separator", content: "[ui.chatbox]\nseparator = \"\\n\"", want: "separator"},
		{name: "unknown scrollback", content: `[ui.chatbox]
scrollback = "cards"`, want: "scrollback"},
		{name: "unknown path color mode", content: `[ui.chatbox]
path-color-mode = "rainbow"`, want: "path-color-mode"},
		{name: "too few path segments", content: `[ui.chatbox]
path-max-segments = 2`, want: "path-max-segments"},
		{name: "too much history spacing", content: `[ui.chatbox]
history-spacing = 4`, want: "history-spacing"},
		{name: "fast refresh", content: `[ui.chatbox]
refresh-interval = "10ms"`, want: "refresh-interval"},
		{name: "invalid metric hysteresis", content: `[ui.chatbox]
metric-hysteresis = 0`, want: "metric-hysteresis"},
		{name: "invalid brand color", content: `[ui.chatbox.colors]
rust = "orange"`, want: "rust"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configRoot := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configRoot)
			configDir := filepath.Join(configRoot, "vuja")
			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation error, got %v", test.want, err)
			}
		})
	}
}

func TestCustomDuration(t *testing.T) {
	var dur Duration
	err := dur.UnmarshalText([]byte("6h"))
	if err != nil {
		t.Fatalf("unexpected error unmarshalling duration: %v", err)
	}
	if time.Duration(dur) != 6*time.Hour {
		t.Errorf("expected 6 hours, got %v", time.Duration(dur))
	}

	b, err := dur.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error marshaling duration: %v", err)
	}
	if string(b) != "6h0m0s" {
		t.Errorf("expected 6h0m0s, got %q", string(b))
	}

	err = dur.UnmarshalText([]byte("invalid"))
	if err == nil {
		t.Errorf("expected error for invalid duration")
	}
}

func TestValidationAndEnvironmentOverrides(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vuja-config-env-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, "vuja")
	if mkErr := os.MkdirAll(configDir, 0755); mkErr != nil {
		t.Fatalf("failed to create config dir: %v", mkErr)
	}
	configPath := filepath.Join(configDir, "config.toml")
	tomlContent := `
[ai]
enabled = true
provider = "groq"

[ai.providers.groq]
inherited_from = "openai"
endpoint = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
model = "qwen-2.5-coder-32b"
`
	if wrErr := os.WriteFile(configPath, []byte(tomlContent), 0644); wrErr != nil {
		t.Fatalf("failed to write config file: %v", wrErr)
	}

	t.Setenv("VUJA_CORE_DEBUG", "true")
	t.Setenv("VUJA_CORE_SHELL", "fish")
	t.Setenv("VUJA_CORE_MODE", "history")
	t.Setenv("VUJA_UI_GHOST_TEXT", "false")
	t.Setenv("VUJA_UI_PROMPT_POSITION", "bottom")
	t.Setenv("VUJA_UI_MAX_SUGGESTIONS", "250")
	t.Setenv("VUJA_UI_MAX_HEIGHT", "25")
	t.Setenv("VUJA_UPDATER_CHANNEL", "nightly")
	t.Setenv("VUJA_UPDATER_INTERVAL", "12h")
	t.Setenv("VUJA_UPDATER_CHECK_ON_STARTUP", "false")
	t.Setenv("VUJA_AI_PROVIDER", "ollama")
	t.Setenv("GROQ_API_KEY", "gsk_test_123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if !cfg.Core.Debug {
		t.Errorf("expected debug to be true")
	}
	if cfg.Core.Shell != "fish" {
		t.Errorf("expected shell fish, got %q", cfg.Core.Shell)
	}
	if cfg.Core.Mode != "history" {
		t.Errorf("expected mode history, got %q", cfg.Core.Mode)
	}
	if cfg.UI.GhostText {
		t.Errorf("expected ghost text to be false")
	}
	if cfg.UI.PromptPosition != "bottom" {
		t.Errorf("expected bottom prompt position, got %q", cfg.UI.PromptPosition)
	}
	if cfg.UI.MaxSuggestions != 250 {
		t.Errorf("expected max suggestions 250, got %d", cfg.UI.MaxSuggestions)
	}
	if cfg.UI.MaxHeight != 25 {
		t.Errorf("expected max height 25, got %d", cfg.UI.MaxHeight)
	}
	if cfg.Updater.Channel != "nightly" {
		t.Errorf("expected channel nightly, got %q", cfg.Updater.Channel)
	}
	if time.Duration(cfg.Updater.CheckInterval) != 12*time.Hour {
		t.Errorf("expected 12h, got %v", time.Duration(cfg.Updater.CheckInterval))
	}
	if cfg.Updater.CheckOnStartup {
		t.Errorf("expected check on startup to be false")
	}
	if cfg.AI.Provider != "ollama" {
		t.Errorf("expected provider ollama from env, got %q", cfg.AI.Provider)
	}
	groqCfg := cfg.AI.Providers["groq"]
	if groqCfg.GetAPIKey() != "gsk_test_123" {
		t.Errorf("expected groq api key gsk_test_123 from env, got %q", groqCfg.GetAPIKey())
	}

	t.Setenv("VUJA_CORE_MODE", "invalid")
	_, err = Load()
	if err == nil {
		t.Errorf("expected validation error for invalid mode in env")
	}
}

func TestLoadRejectsInvalidPromptPosition(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui]
prompt-position = "floating"
`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ui.prompt-position") {
		t.Fatalf("expected prompt position validation error, got %v", err)
	}
}

func TestLoadRejectsInvalidDirectoryRanking(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[suggestions]
directory-ranking = "random"
`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "suggestions.directory-ranking") {
		t.Fatalf("expected directory ranking validation error, got %v", err)
	}
}

func TestLoadSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vuja-config-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	cfg.Core.Shell = "zsh"
	cfg.UI.MaxHeight = 20

	err = Save(cfg)
	if err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("failed to load after save: %v", err)
	}

	if loaded.Core.Shell != "zsh" {
		t.Errorf("expected loaded shell to be zsh, got %q", loaded.Core.Shell)
	}
	if loaded.UI.MaxHeight != 20 {
		t.Errorf("expected loaded height to be 20, got %d", loaded.UI.MaxHeight)
	}
}

func TestLoadCustomAdaptiveColors(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[ui.colors.day]
border = "#112233"
accent = "#445566"

[ui.colors.night]
border = "#aabbcc"
accent = "#ddeeff"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Colors.Day.Border != "#112233" || cfg.UI.Colors.Night.Border != "#aabbcc" {
		t.Fatalf("expected custom adaptive border colors, got day=%q night=%q", cfg.UI.Colors.Day.Border, cfg.UI.Colors.Night.Border)
	}
	if cfg.UI.Colors.Day.Accent != "#445566" || cfg.UI.Colors.Night.Accent != "#ddeeff" {
		t.Fatalf("expected custom adaptive accent colors, got day=%q night=%q", cfg.UI.Colors.Day.Accent, cfg.UI.Colors.Night.Accent)
	}
}

func TestLoadRejectsInvalidOverlayColor(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.colors.day]
border = "not-a-color"
`), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("expected invalid overlay color to be rejected")
	}
}

func TestLoadRejectsInvalidOrConflictingKeybindings(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "unknown key",
			content: `
[keybindings]
history-search = "hyper+r"
`,
		},
		{
			name: "conflicting actions",
			content: `
[keybindings]
history-search = "ctrl+r"
accept = "ctrl+r"
`,
		},
		{
			name: "reserved line editing key",
			content: `
[keybindings]
history-search = "ctrl+a"
`,
		},
		{
			name: "multi-byte accept key",
			content: `
[keybindings]
accept = "shift+tab"
toggle-overlay = "ctrl+t"
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configRoot := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configRoot)
			configDir := filepath.Join(configRoot, "vuja")
			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}

			if _, err := Load(); err == nil {
				t.Fatal("expected invalid keybinding configuration to be rejected")
			}
		})
	}
}

func TestLoadCustomKeybindingsAndDisableTokenAcceptance(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[keybindings]
history-search = "ctrl+g"
history-success-only = "none"
accept = "ctrl+n"
toggle-overlay = "ctrl+t"
accept-token = []
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Keybindings.HistorySearch != "ctrl+g" ||
		cfg.Keybindings.HistorySuccessOnly != "none" ||
		cfg.Keybindings.Accept != "ctrl+n" ||
		cfg.Keybindings.ToggleOverlay != "ctrl+t" {
		t.Fatalf("unexpected custom keybindings: %+v", cfg.Keybindings)
	}
	if len(cfg.Keybindings.AcceptToken) != 0 {
		t.Fatalf("expected token acceptance to be disabled, got %v", cfg.Keybindings.AcceptToken)
	}
}

func TestMigration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vuja-migrate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, ".local", "share"))

	legacyDir := filepath.Join(tmpDir, ".vuja")
	if errMkdir := os.MkdirAll(legacyDir, 0755); errMkdir != nil {
		t.Fatalf("failed to create legacy dir: %v", errMkdir)
	}

	legacyStateJson := `{"mode": "history"}`
	_ = os.WriteFile(filepath.Join(legacyDir, "state.json"), []byte(legacyStateJson), 0644)

	legacyUpdateJson := `{"seen_version": "v1.2.3", "last_check": 1234567890}`
	_ = os.WriteFile(filepath.Join(legacyDir, "update_state.json"), []byte(legacyUpdateJson), 0644)

	err = MigrateFromLegacyJSON()
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	state := LoadState()
	if state.LastMode != "history" {
		t.Errorf("expected migrated last mode 'history', got %q", state.LastMode)
	}
	if state.Updater.SeenVersion != "v1.2.3" {
		t.Errorf("expected migrated seen version 'v1.2.3', got %q", state.Updater.SeenVersion)
	}
	if state.Updater.LastCheckTime.Unix() != 1234567890 {
		t.Errorf("expected migrated check time 1234567890, got %v", state.Updater.LastCheckTime.Unix())
	}

	if _, err := os.Stat(filepath.Join(legacyDir, "state.json.bak")); err != nil {
		t.Errorf("expected backup file state.json.bak to exist")
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "update_state.json.bak")); err != nil {
		t.Errorf("expected backup file update_state.json.bak to exist")
	}
}

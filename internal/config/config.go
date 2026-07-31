package config

import (
	"encoding"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

type Duration time.Duration

var (
	_ encoding.TextUnmarshaler = (*Duration)(nil)
	_ encoding.TextMarshaler   = (*Duration)(nil)
)

func (d *Duration) UnmarshalText(text []byte) error {
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

type CoreConfig struct {
	Version int    `toml:"version"`
	Shell   string `toml:"shell"`
	Mode    string `toml:"mode"`
	Debug   bool   `toml:"debug"`
}

type UIConfig struct {
	Style          string          `toml:"style"`
	PromptPosition string          `toml:"prompt-position"`
	GhostText      bool            `toml:"ghost-text"`
	MaxSuggestions int             `toml:"max-suggestions"`
	MaxHeight      int             `toml:"max-height"`
	NerdFonts      bool            `toml:"nerd-fonts"`
	History        HistoryUIConfig `toml:"history"`
	Chatbox        ChatboxConfig   `toml:"chatbox"`
	Colors         ColorsConfig    `toml:"colors"`
}

type ChatboxConfig struct {
	Prompt           string              `toml:"prompt"`
	Separator        string              `toml:"separator"`
	Scrollback       string              `toml:"scrollback"`
	PathColorMode    string              `toml:"path-color-mode"`
	PathMaxSegments  int                 `toml:"path-max-segments"`
	HistorySpacing   int                 `toml:"history-spacing"`
	Status           []string            `toml:"status,omitempty"` // Legacy single-row layout.
	TitleLeft        []string            `toml:"title-left"`
	TitleCenter      []string            `toml:"title-center"`
	TitleRight       []string            `toml:"title-right"`
	StatusLeft       []string            `toml:"status-left"`
	StatusCenter     []string            `toml:"status-center"`
	StatusRight      []string            `toml:"status-right"`
	RefreshInterval  Duration            `toml:"refresh-interval"`
	CollapseVersions bool                `toml:"collapse-versions"`
	Colors           ChatboxColorsConfig `toml:"colors"`
}

func (c ChatboxConfig) Segments() []string {
	segments := make([]string, 0, len(c.Status)+len(c.TitleLeft)+len(c.TitleCenter)+len(c.TitleRight)+
		len(c.StatusLeft)+len(c.StatusCenter)+len(c.StatusRight))
	for _, region := range [][]string{
		c.TitleLeft, c.TitleCenter, c.TitleRight,
		c.StatusLeft, c.StatusCenter, c.StatusRight,
	} {
		segments = append(segments, region...)
	}
	if len(segments) == 0 {
		segments = append(segments, c.Status...)
	}
	return segments
}

type ChatboxColorsConfig struct {
	Directory         string `toml:"directory"`
	DirectoryRoot     string `toml:"directory-root"`
	DirectoryReadOnly string `toml:"directory-read-only"`
	GitBranch         string `toml:"git-branch"`
	GitStatus         string `toml:"git-status"`
	GitClean          string `toml:"git-clean"`
	GitOperation      string `toml:"git-operation"`
	GitConflicts      string `toml:"git-conflicts"`
	GitStaged         string `toml:"git-staged"`
	GitModified       string `toml:"git-modified"`
	GitRenamed        string `toml:"git-renamed"`
	GitUntracked      string `toml:"git-untracked"`
	GitAhead          string `toml:"git-ahead"`
	GitBehind         string `toml:"git-behind"`
	GitAdded          string `toml:"git-added"`
	GitDeleted        string `toml:"git-deleted"`
	GitStash          string `toml:"git-stash"`
	GitLinesAdded     string `toml:"git-lines-added"`
	GitLinesDeleted   string `toml:"git-lines-deleted"`
	Session           string `toml:"session"`
	SessionWarning    string `toml:"session-warning"`
	SessionCritical   string `toml:"session-critical"`
	Contexts          string `toml:"contexts"`
	Environment       string `toml:"environment"`
	VersionMismatch   string `toml:"version-mismatch"`
	Package           string `toml:"package"`
	Stale             string `toml:"stale"`
	Jobs              string `toml:"jobs"`
	JobsStopped       string `toml:"jobs-stopped"`
	ExitNeutral       string `toml:"exit-neutral"`
	ExitSuccess       string `toml:"exit-success"`
	ExitFailure       string `toml:"exit-failure"`
	Duration          string `toml:"duration"`
	DurationFast      string `toml:"duration-fast"`
	DurationAverage   string `toml:"duration-average"`
	DurationSlow      string `toml:"duration-slow"`
	LoadLow           string `toml:"load-low"`
	LoadAverage       string `toml:"load-average"`
	LoadHigh          string `toml:"load-high"`
	LoadCritical      string `toml:"load-critical"`
	Laravel           string `toml:"laravel"`
	PHP               string `toml:"php"`
	Composer          string `toml:"composer"`
	Python            string `toml:"python"`
	Ruby              string `toml:"ruby"`
	Elixir            string `toml:"elixir"`
	Go                string `toml:"go"`
	Node              string `toml:"node"`
	Bun               string `toml:"bun"`
	Rust              string `toml:"rust"`
	Docker            string `toml:"docker"`
	DockerCompose     string `toml:"docker-compose"`
	CPU               string `toml:"cpu"`
	Memory            string `toml:"memory"`
	UseLegacyCPU      bool   `toml:"-"`
	UseLegacyMemory   bool   `toml:"-"`
}

type HistoryUIConfig struct {
	ShowExitStatus bool `toml:"show-exit-status"`
	ShowCwd        bool `toml:"show-cwd"`
}

type ColorsConfig struct {
	Day   ColorPaletteConfig `toml:"day"`
	Night ColorPaletteConfig `toml:"night"`
}

type ColorPaletteConfig struct {
	Background                 string `toml:"background"`
	Border                     string `toml:"border"`
	Accent                     string `toml:"accent"`
	Muted                      string `toml:"muted"`
	Text                       string `toml:"text"`
	TextSelected               string `toml:"text-selected"`
	Match                      string `toml:"match"`
	Description                string `toml:"description"`
	DescriptionSelected        string `toml:"description-selected"`
	SelectionBackground        string `toml:"selection-background"`
	SurfaceBackground          string `toml:"surface-background"`
	CompletedSurfaceBackground string `toml:"completed-surface-background"`
	StatusBackground           string `toml:"status-background"`
	StatusText                 string `toml:"status-text"`
	ScrollInfo                 string `toml:"scroll-info"`
	GhostText                  string `toml:"ghost-text"`
}

type GitConfig struct {
	FilterActiveBranch  bool `toml:"filter-active-branch"`
	DeduplicateBranches bool `toml:"deduplicate-branches"`
}

type UpdaterConfig struct {
	CheckOnStartup bool     `toml:"check-on-startup"`
	Channel        string   `toml:"channel"`
	CheckInterval  Duration `toml:"check-interval"`
}

type SuggestOnEmptyConfig struct {
	Enabled       bool `toml:"enabled"`
	DebounceMS    int  `toml:"debounce_ms"`
	MinIntervalMS int  `toml:"min_interval_ms"`
}

type ProviderConfig struct {
	InheritedFrom    string         `toml:"inherited_from"`
	Endpoint         string         `toml:"endpoint"`
	APIKey           string         `toml:"api_key"`
	APIKeyEnv        string         `toml:"api_key_env"`
	Model            string         `toml:"model"`
	TimeoutMS        int            `toml:"timeout_ms"`
	ExtraRequestBody map[string]any `toml:"extra_request_body"`
}

type AIConfig struct {
	Enabled        bool                      `toml:"enabled"`
	Provider       string                    `toml:"provider"`
	DebounceMS     int                       `toml:"debounce_ms"`
	MinIntervalMS  int                       `toml:"min_interval_ms"`
	Providers      map[string]ProviderConfig `toml:"providers"`
	SuggestOnEmpty SuggestOnEmptyConfig      `toml:"suggest_on_empty"`
}

type SuggestionsConfig struct {
	Pins                []string `toml:"pins"`
	Blocks              []string `toml:"blocks"`
	IgnorePatterns      []string `toml:"ignore-patterns"`
	SuppressDestructive bool     `toml:"suppress-destructive"`
}

type KeybindingsConfig struct {
	HistorySearch      string   `toml:"history-search"`
	HistorySuccessOnly string   `toml:"history-success-only"`
	Accept             string   `toml:"accept"`
	ToggleOverlay      string   `toml:"toggle-overlay"`
	AcceptToken        []string `toml:"accept-token"`
}

func (c *AIConfig) GetActiveProvider() (ProviderConfig, bool) {
	if c.Providers == nil {
		return ProviderConfig{}, false
	}
	p, ok := c.Providers[c.Provider]
	return p, ok
}

func (p *ProviderConfig) GetAPIKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}

type Config struct {
	Core        CoreConfig        `toml:"core"`
	UI          UIConfig          `toml:"ui"`
	Git         GitConfig         `toml:"git"`
	Updater     UpdaterConfig     `toml:"updater"`
	AI          AIConfig          `toml:"ai"`
	Suggestions SuggestionsConfig `toml:"suggestions"`
	Keybindings KeybindingsConfig `toml:"keybindings"`
}

var (
	activeConfig *Config
	once         sync.Once
)

func Get() *Config {
	once.Do(func() {
		if activeConfig == nil {
			activeConfig = DefaultConfig()
		}
	})
	return activeConfig
}

func Init(cfg *Config) {
	activeConfig = cfg
	once.Do(func() {})
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err == nil {
		if _, statErr := os.Stat(path); statErr == nil {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return cfg, fmt.Errorf("config: read %s: %w", path, readErr)
			}
			metadata, decodeErr := toml.Decode(string(data), cfg)
			if decodeErr != nil {
				return cfg, fmt.Errorf("config: parse %s: %w", path, decodeErr)
			}
			if err := applyLegacyChatboxLayout(cfg, &metadata); err != nil {
				return cfg, fmt.Errorf("config: invalid value: %w", err)
			}
			applyLegacyChatboxColors(cfg, &metadata)
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return cfg, fmt.Errorf("config: invalid value: %w", err)
	}

	return cfg, nil
}

func applyLegacyChatboxLayout(cfg *Config, metadata *toml.MetaData) error {
	if cfg == nil || metadata == nil {
		return nil
	}
	if !metadata.IsDefined("ui", "chatbox", "status") {
		applyPreviousDefaultChatboxLayout(cfg, metadata)
		return nil
	}
	for _, key := range []string{"title-left", "title-center", "title-right", "status-left", "status-center", "status-right"} {
		if metadata.IsDefined("ui", "chatbox", key) {
			return fmt.Errorf("ui.chatbox.status: cannot be combined with ui.chatbox.%s", key)
		}
	}
	legacyDefault := []string{"directory", "git-branch", "git-status", "git-added", "git-deleted", "duration", "exit", "versions", "cpu", "memory"}
	if slices.Equal(cfg.UI.Chatbox.Status, legacyDefault) {
		defaults := DefaultConfig().UI.Chatbox
		cfg.UI.Chatbox.TitleLeft = append([]string(nil), defaults.TitleLeft...)
		cfg.UI.Chatbox.TitleCenter = append([]string(nil), defaults.TitleCenter...)
		cfg.UI.Chatbox.TitleRight = append([]string(nil), defaults.TitleRight...)
		cfg.UI.Chatbox.StatusLeft = append([]string(nil), defaults.StatusLeft...)
		cfg.UI.Chatbox.StatusCenter = append([]string(nil), defaults.StatusCenter...)
		cfg.UI.Chatbox.StatusRight = append([]string(nil), defaults.StatusRight...)
		cfg.UI.Chatbox.Status = nil
		return nil
	}
	cfg.UI.Chatbox.TitleLeft = nil
	cfg.UI.Chatbox.TitleCenter = nil
	cfg.UI.Chatbox.TitleRight = nil
	cfg.UI.Chatbox.StatusLeft = nil
	cfg.UI.Chatbox.StatusCenter = nil
	cfg.UI.Chatbox.StatusRight = nil
	for _, segment := range cfg.UI.Chatbox.Status {
		switch segment {
		case "jobs", "duration", "exit", "cpu", "memory":
			cfg.UI.Chatbox.StatusRight = append(cfg.UI.Chatbox.StatusRight, segment)
		default:
			cfg.UI.Chatbox.StatusLeft = append(cfg.UI.Chatbox.StatusLeft, segment)
		}
	}
	cfg.UI.Chatbox.Status = nil
	return nil
}

func applyPreviousDefaultChatboxLayout(cfg *Config, metadata *toml.MetaData) {
	for _, key := range []string{"title-left", "title-center", "title-right", "status-left", "status-center", "status-right"} {
		if !metadata.IsDefined("ui", "chatbox", key) {
			return
		}
	}
	if !slices.Equal(cfg.UI.Chatbox.TitleLeft, []string{"directory"}) ||
		len(cfg.UI.Chatbox.TitleCenter) != 0 ||
		!slices.Equal(cfg.UI.Chatbox.TitleRight, []string{"versions"}) ||
		!slices.Equal(cfg.UI.Chatbox.StatusLeft, []string{"git-branch", "git-status", "git-added", "git-deleted"}) ||
		len(cfg.UI.Chatbox.StatusCenter) != 0 ||
		!slices.Equal(cfg.UI.Chatbox.StatusRight, []string{"duration", "exit", "cpu", "memory"}) {
		return
	}
	defaults := DefaultConfig().UI.Chatbox
	cfg.UI.Chatbox.TitleLeft = append([]string(nil), defaults.TitleLeft...)
	cfg.UI.Chatbox.TitleCenter = append([]string(nil), defaults.TitleCenter...)
	cfg.UI.Chatbox.TitleRight = append([]string(nil), defaults.TitleRight...)
	cfg.UI.Chatbox.StatusLeft = append([]string(nil), defaults.StatusLeft...)
	cfg.UI.Chatbox.StatusCenter = append([]string(nil), defaults.StatusCenter...)
	cfg.UI.Chatbox.StatusRight = append([]string(nil), defaults.StatusRight...)
}

func applyLegacyChatboxColors(cfg *Config, metadata *toml.MetaData) {
	if cfg == nil || metadata == nil {
		return
	}
	defined := func(name string) bool {
		return metadata.IsDefined("ui", "chatbox", "colors", name)
	}
	colors := &cfg.UI.Chatbox.Colors
	if defined("git-status") {
		for name, target := range map[string]*string{
			"git-clean": &colors.GitClean, "git-operation": &colors.GitOperation,
			"git-conflicts": &colors.GitConflicts, "git-staged": &colors.GitStaged,
			"git-modified": &colors.GitModified, "git-renamed": &colors.GitRenamed,
			"git-untracked": &colors.GitUntracked,
			"git-ahead":     &colors.GitAhead, "git-behind": &colors.GitBehind,
		} {
			if !defined(name) {
				*target = colors.GitStatus
			}
		}
	}
	if defined("duration") {
		for name, target := range map[string]*string{
			"duration-fast": &colors.DurationFast, "duration-average": &colors.DurationAverage,
			"duration-slow": &colors.DurationSlow,
		} {
			if !defined(name) {
				*target = colors.Duration
			}
		}
	}
	loadDefined := anyDefined(defined, "load-low", "load-average", "load-high", "load-critical")
	colors.UseLegacyCPU = defined("cpu") && !loadDefined
	colors.UseLegacyMemory = defined("memory") && !loadDefined
}

func anyDefined(defined func(string) bool, names ...string) bool {
	for _, name := range names {
		if defined(name) {
			return true
		}
	}
	return false
}

func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	enc := toml.NewEncoder(file)
	if err := enc.Encode(cfg); err != nil {
		return err
	}

	return nil
}

func validate(cfg *Config) error {
	validModes := map[string]bool{"last": true, "spec": true, "history": true}
	if cfg.Core.Mode != "" && !validModes[cfg.Core.Mode] {
		return fmt.Errorf("core.mode: invalid value %q (want: last|spec|history)", cfg.Core.Mode)
	}

	validShells := map[string]bool{"": true, "bash": true, "zsh": true, "fish": true}
	if !validShells[cfg.Core.Shell] {
		return fmt.Errorf("core.shell: invalid value %q (want: bash|zsh|fish)", cfg.Core.Shell)
	}

	validChannels := map[string]bool{"stable": true, "nightly": true}
	if !validChannels[cfg.Updater.Channel] {
		return fmt.Errorf("updater.channel: invalid value %q (want: stable|nightly)", cfg.Updater.Channel)
	}

	if cfg.UI.MaxSuggestions < 1 || cfg.UI.MaxSuggestions > 500 {
		return fmt.Errorf("ui.max-suggestions: must be between 1 and 500")
	}

	if cfg.UI.MaxHeight < 3 || cfg.UI.MaxHeight > 50 {
		return fmt.Errorf("ui.max-height: must be between 3 and 50")
	}

	validPromptPositions := map[string]bool{"classic": true, "bottom": true}
	if !validPromptPositions[cfg.UI.PromptPosition] {
		return fmt.Errorf("ui.prompt-position: invalid value %q (want: classic|bottom)", cfg.UI.PromptPosition)
	}

	if err := validateChatbox(cfg.UI.Chatbox); err != nil {
		return err
	}

	for _, palette := range []struct {
		name   string
		colors ColorPaletteConfig
	}{
		{name: "day", colors: cfg.UI.Colors.Day},
		{name: "night", colors: cfg.UI.Colors.Night},
	} {
		for _, color := range palette.colors.namedColors() {
			if !hexColorPattern.MatchString(color.value) {
				return fmt.Errorf("ui.colors.%s.%s: invalid color %q (want: #RRGGBB)", palette.name, color.name, color.value)
			}
		}
	}

	if err := validateKeybindings(cfg.Keybindings); err != nil {
		return err
	}

	return nil
}

var hexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func (p ColorPaletteConfig) namedColors() []struct {
	name  string
	value string
} {
	return []struct {
		name  string
		value string
	}{
		{name: "background", value: p.Background},
		{name: "border", value: p.Border},
		{name: "accent", value: p.Accent},
		{name: "muted", value: p.Muted},
		{name: "text", value: p.Text},
		{name: "text-selected", value: p.TextSelected},
		{name: "match", value: p.Match},
		{name: "description", value: p.Description},
		{name: "description-selected", value: p.DescriptionSelected},
		{name: "selection-background", value: p.SelectionBackground},
		{name: "surface-background", value: p.SurfaceBackground},
		{name: "completed-surface-background", value: p.CompletedSurfaceBackground},
		{name: "status-background", value: p.StatusBackground},
		{name: "status-text", value: p.StatusText},
		{name: "scroll-info", value: p.ScrollInfo},
		{name: "ghost-text", value: p.GhostText},
	}
}

func validateChatbox(chatbox ChatboxConfig) error {
	if strings.TrimSpace(chatbox.Prompt) == "" || !plainChatboxText(chatbox.Prompt) || strings.ContainsAny(chatbox.Prompt, "$`%\\") {
		return fmt.Errorf("ui.chatbox.prompt: must be non-empty plain text without ANSI escapes or command substitutions")
	}
	if !plainChatboxText(chatbox.Separator) {
		return fmt.Errorf("ui.chatbox.separator: must be plain single-line text")
	}
	if chatbox.Scrollback != "output" && chatbox.Scrollback != "snapshot" {
		return fmt.Errorf("ui.chatbox.scrollback: invalid value %q (want: output|snapshot)", chatbox.Scrollback)
	}
	if chatbox.PathColorMode != "single" && chatbox.PathColorMode != "hierarchy" {
		return fmt.Errorf("ui.chatbox.path-color-mode: invalid value %q (want: single|hierarchy)", chatbox.PathColorMode)
	}
	if chatbox.PathMaxSegments != 0 && (chatbox.PathMaxSegments < 3 || chatbox.PathMaxSegments > 64) {
		return fmt.Errorf("ui.chatbox.path-max-segments: must be 0 or between 3 and 64")
	}
	if chatbox.HistorySpacing < 0 || chatbox.HistorySpacing > 3 {
		return fmt.Errorf("ui.chatbox.history-spacing: must be between 0 and 3")
	}
	validSegments := map[string]bool{
		"directory": true, "git-branch": true, "git-status": true,
		"git-added": true, "git-deleted": true, "exit": true,
		"duration": true, "versions": true, "cpu": true, "memory": true,
		"session": true, "git-stash": true, "git-lines": true,
		"environment": true, "version-mismatch": true, "contexts": true,
		"package": true, "stale": true, "jobs": true,
	}
	segments := chatbox.Segments()
	seen := make(map[string]bool, len(segments))
	for _, segment := range segments {
		if !validSegments[segment] {
			return fmt.Errorf("ui.chatbox: unknown status segment %q", segment)
		}
		if seen[segment] {
			return fmt.Errorf("ui.chatbox: duplicate status segment %q", segment)
		}
		seen[segment] = true
	}
	refresh := time.Duration(chatbox.RefreshInterval)
	if refresh < 250*time.Millisecond || refresh > time.Minute {
		return fmt.Errorf("ui.chatbox.refresh-interval: must be between 250ms and 1m")
	}
	for _, color := range chatbox.Colors.namedColors() {
		if !hexColorPattern.MatchString(color.value) {
			return fmt.Errorf("ui.chatbox.colors.%s: invalid color %q (want: #RRGGBB)", color.name, color.value)
		}
	}
	return nil
}

func plainChatboxText(value string) bool {
	for _, char := range value {
		if char == '\x1b' || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (c ChatboxColorsConfig) namedColors() []struct {
	name  string
	value string
} {
	return []struct {
		name  string
		value string
	}{
		{"directory", c.Directory}, {"directory-root", c.DirectoryRoot},
		{"directory-read-only", c.DirectoryReadOnly}, {"git-branch", c.GitBranch},
		{"git-status", c.GitStatus}, {"git-clean", c.GitClean},
		{"git-operation", c.GitOperation}, {"git-conflicts", c.GitConflicts},
		{"git-staged", c.GitStaged}, {"git-modified", c.GitModified},
		{"git-renamed", c.GitRenamed},
		{"git-untracked", c.GitUntracked}, {"git-ahead", c.GitAhead},
		{"git-behind", c.GitBehind}, {"git-added", c.GitAdded},
		{"git-deleted", c.GitDeleted}, {"git-stash", c.GitStash},
		{"git-lines-added", c.GitLinesAdded}, {"git-lines-deleted", c.GitLinesDeleted},
		{"session", c.Session}, {"session-warning", c.SessionWarning},
		{"session-critical", c.SessionCritical}, {"contexts", c.Contexts},
		{"environment", c.Environment}, {"version-mismatch", c.VersionMismatch},
		{"package", c.Package}, {"stale", c.Stale}, {"jobs", c.Jobs},
		{"jobs-stopped", c.JobsStopped}, {"exit-neutral", c.ExitNeutral},
		{"exit-success", c.ExitSuccess},
		{"exit-failure", c.ExitFailure}, {"duration", c.Duration},
		{"duration-fast", c.DurationFast}, {"duration-average", c.DurationAverage},
		{"duration-slow", c.DurationSlow}, {"load-low", c.LoadLow},
		{"load-average", c.LoadAverage}, {"load-high", c.LoadHigh},
		{"load-critical", c.LoadCritical},
		{"laravel", c.Laravel}, {"php", c.PHP}, {"composer", c.Composer},
		{"python", c.Python}, {"ruby", c.Ruby}, {"elixir", c.Elixir},
		{"go", c.Go}, {"node", c.Node}, {"bun", c.Bun}, {"rust", c.Rust},
		{"docker", c.Docker}, {"docker-compose", c.DockerCompose},
		{"cpu", c.CPU}, {"memory", c.Memory},
	}
}

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
	Density        string          `toml:"density"`
	Responsive     bool            `toml:"responsive"`
	Palette        string          `toml:"palette"`
	Contrast       string          `toml:"contrast"`
	ColorVision    string          `toml:"color-vision"`
	GhostText      bool            `toml:"ghost-text"`
	MaxSuggestions int             `toml:"max-suggestions"`
	MaxHeight      int             `toml:"max-height"`
	NerdFonts      bool            `toml:"nerd-fonts"`
	History        HistoryUIConfig `toml:"history"`
	Chatbox        ChatboxConfig   `toml:"chatbox"`
	Colors         ColorsConfig    `toml:"colors"`
}

type ChatboxConfig struct {
	Prompt            string              `toml:"prompt"`
	Separator         string              `toml:"separator"`
	Scrollback        string              `toml:"scrollback"`
	PathColorMode     string              `toml:"path-color-mode"`
	PathMaxSegments   int                 `toml:"path-max-segments"`
	HistorySpacing    int                 `toml:"history-spacing"`
	Status            []string            `toml:"status,omitempty"` // Legacy single-row layout.
	TitleLeft         []string            `toml:"title-left"`
	TitleCenter       []string            `toml:"title-center"`
	TitleRight        []string            `toml:"title-right"`
	StatusLeft        []string            `toml:"status-left"`
	StatusCenter      []string            `toml:"status-center"`
	StatusRight       []string            `toml:"status-right"`
	RefreshInterval   Duration            `toml:"refresh-interval"`
	MetricHysteresis  float64             `toml:"metric-hysteresis"`
	CollapseVersions  bool                `toml:"collapse-versions"`
	SnapshotMetadata  string              `toml:"snapshot-metadata"`
	CompletedCommand  string              `toml:"completed-command"`
	Metrics           string              `toml:"metrics"`
	Versions          string              `toml:"versions"`
	VersionAllow      []string            `toml:"version-allow"`
	VersionDeny       []string            `toml:"version-deny"`
	DockerContext     string              `toml:"docker-context"`
	KubernetesContext string              `toml:"kubernetes-context"`
	AWSContext        string              `toml:"aws-context"`
	DurationFast      Duration            `toml:"duration-fast"`
	DurationSlow      Duration            `toml:"duration-slow"`
	CPUAverage        int                 `toml:"cpu-average"`
	CPUHigh           int                 `toml:"cpu-high"`
	CPUCritical       int                 `toml:"cpu-critical"`
	MemoryAverage     int                 `toml:"memory-average"`
	MemoryHigh        int                 `toml:"memory-high"`
	MemoryCritical    int                 `toml:"memory-critical"`
	Colors            ChatboxColorsConfig `toml:"colors"`
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
	DirectoryRanking    string   `toml:"directory-ranking"`
}

type KeybindingsConfig struct {
	Keymap             string   `toml:"keymap"`
	MoveBeginning      string   `toml:"move-beginning"`
	MoveEnd            string   `toml:"move-end"`
	ClearScreen        string   `toml:"clear-screen"`
	ClearLine          string   `toml:"clear-line"`
	Cancel             string   `toml:"cancel"`
	DeleteWord         string   `toml:"delete-word"`
	HistorySearch      string   `toml:"history-search"`
	HistorySuccessOnly string   `toml:"history-success-only"`
	Accept             string   `toml:"accept"`
	ToggleOverlay      string   `toml:"toggle-overlay"`
	AcceptToken        []string `toml:"accept-token"`
}

func (c KeybindingsConfig) ResolvedLineEditingBindings() map[string]string {
	defaults := map[string]string{
		"move-beginning": "ctrl+a", "move-end": "ctrl+e", "clear-screen": "ctrl+l",
		"clear-line": "ctrl+u", "cancel": "ctrl+c", "delete-word": "ctrl+w",
	}
	if c.Keymap == "vi" {
		defaults["move-beginning"] = "home"
		defaults["move-end"] = "end"
	}
	for name, value := range map[string]string{
		"move-beginning": c.MoveBeginning, "move-end": c.MoveEnd, "clear-screen": c.ClearScreen,
		"clear-line": c.ClearLine, "cancel": c.Cancel, "delete-word": c.DeleteWord,
	} {
		if strings.TrimSpace(value) != "" {
			defaults[name] = value
		}
	}
	return defaults
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
			if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
				return cfg, err
			}
			if err := RestrictPrivateFiles(path); err != nil {
				return cfg, err
			}
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
			applySchemaCompatibility(cfg, &metadata)
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return cfg, fmt.Errorf("config: invalid value: %w", err)
	}

	return cfg, nil
}

func applySchemaCompatibility(cfg *Config, metadata *toml.MetaData) {
	if cfg == nil || metadata == nil {
		return
	}
	schemaVersion := cfg.Core.Version
	if !metadata.IsDefined("core", "version") {
		schemaVersion = 1
	}
	if schemaVersion >= 2 {
		return
	}
	if !metadata.IsDefined("ui", "chatbox", "completed-command") {
		cfg.UI.Chatbox.CompletedCommand = "snapshot"
	}
	if !metadata.IsDefined("ui", "chatbox", "snapshot-metadata") {
		cfg.UI.Chatbox.SnapshotMetadata = "always"
	}
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

	if err = EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}

	file, err := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
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
	if cfg.Core.Version < 1 || cfg.Core.Version > CurrentVersion {
		return fmt.Errorf("core.version: unsupported schema %d (current: %d)", cfg.Core.Version, CurrentVersion)
	}
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
	if !oneOf(cfg.UI.Density, "compact", "balanced", "rich") {
		return fmt.Errorf("ui.density: invalid value %q (want: compact|balanced|rich)", cfg.UI.Density)
	}
	if !oneOf(cfg.UI.Palette, "auto", "serein-day", "serein-night", "terminal") {
		return fmt.Errorf("ui.palette: invalid value %q (want: auto|serein-day|serein-night|terminal)", cfg.UI.Palette)
	}
	if !oneOf(cfg.UI.Contrast, "normal", "high") {
		return fmt.Errorf("ui.contrast: invalid value %q (want: normal|high)", cfg.UI.Contrast)
	}
	if !oneOf(cfg.UI.ColorVision, "default", "deuteranopia", "protanopia", "monochrome") {
		return fmt.Errorf("ui.color-vision: invalid value %q (want: default|deuteranopia|protanopia|monochrome)", cfg.UI.ColorVision)
	}
	if !oneOf(cfg.Suggestions.DirectoryRanking, "balanced", "recent", "frequent") {
		return fmt.Errorf("suggestions.directory-ranking: invalid value %q (want: balanced|recent|frequent)", cfg.Suggestions.DirectoryRanking)
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
	if chatbox.MetricHysteresis <= 0 || chatbox.MetricHysteresis > 20 {
		return fmt.Errorf("ui.chatbox.metric-hysteresis: must be greater than 0 and at most 20")
	}
	if !oneOf(chatbox.SnapshotMetadata, "always", "changed", "never") {
		return fmt.Errorf("ui.chatbox.snapshot-metadata: invalid value %q (want: always|changed|never)", chatbox.SnapshotMetadata)
	}
	if !oneOf(chatbox.CompletedCommand, "command", "outcome", "snapshot") {
		return fmt.Errorf("ui.chatbox.completed-command: invalid value %q (want: command|outcome|snapshot)", chatbox.CompletedCommand)
	}
	if !oneOf(chatbox.Metrics, "always", "when-high", "never") {
		return fmt.Errorf("ui.chatbox.metrics: invalid value %q (want: always|when-high|never)", chatbox.Metrics)
	}
	if !oneOf(chatbox.Versions, "auto", "always", "never") {
		return fmt.Errorf("ui.chatbox.versions: invalid value %q (want: auto|always|never)", chatbox.Versions)
	}
	for name, policy := range map[string]string{
		"docker-context":     chatbox.DockerContext,
		"kubernetes-context": chatbox.KubernetesContext,
		"aws-context":        chatbox.AWSContext,
	} {
		if !oneOf(policy, "auto", "always", "never") {
			return fmt.Errorf("ui.chatbox.%s: invalid value %q (want: auto|always|never)", name, policy)
		}
	}
	if time.Duration(chatbox.DurationFast) <= 0 || time.Duration(chatbox.DurationSlow) <= time.Duration(chatbox.DurationFast) {
		return fmt.Errorf("ui.chatbox: duration thresholds must satisfy 0 < duration-fast < duration-slow")
	}
	for name, values := range map[string][3]int{
		"cpu":    {chatbox.CPUAverage, chatbox.CPUHigh, chatbox.CPUCritical},
		"memory": {chatbox.MemoryAverage, chatbox.MemoryHigh, chatbox.MemoryCritical},
	} {
		if values[0] < 1 || values[0] >= values[1] || values[1] >= values[2] || values[2] > 100 {
			return fmt.Errorf("ui.chatbox: %s thresholds must satisfy 0 < average < high < critical <= 100", name)
		}
	}
	validVersionProviders := map[string]bool{"laravel": true, "php": true, "composer": true, "python": true, "ruby": true, "elixir": true, "node": true, "bun": true, "go": true, "rust": true, "docker-compose": true, "docker": true}
	seenVersions := map[string]string{}
	for listName, providers := range map[string][]string{"version-allow": chatbox.VersionAllow, "version-deny": chatbox.VersionDeny} {
		for _, provider := range providers {
			if !validVersionProviders[provider] {
				return fmt.Errorf("ui.chatbox.%s: unknown provider %q", listName, provider)
			}
			if previous := seenVersions[provider]; previous != "" {
				return fmt.Errorf("ui.chatbox.%s: provider %q already appears in %s", listName, provider, previous)
			}
			seenVersions[provider] = listName
		}
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

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
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

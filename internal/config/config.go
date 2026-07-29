package config

import (
	"encoding"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

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
	Style          string       `toml:"style"`
	GhostText      bool         `toml:"ghost-text"`
	MaxSuggestions int          `toml:"max-suggestions"`
	MaxHeight      int          `toml:"max-height"`
	NerdFonts      bool         `toml:"nerd-fonts"`
	Colors         ColorsConfig `toml:"colors"`
}

type ColorsConfig struct {
	Day   ColorPaletteConfig `toml:"day"`
	Night ColorPaletteConfig `toml:"night"`
}

type ColorPaletteConfig struct {
	Background          string `toml:"background"`
	Border              string `toml:"border"`
	Accent              string `toml:"accent"`
	Muted               string `toml:"muted"`
	Text                string `toml:"text"`
	TextSelected        string `toml:"text-selected"`
	Match               string `toml:"match"`
	Description         string `toml:"description"`
	DescriptionSelected string `toml:"description-selected"`
	SelectionBackground string `toml:"selection-background"`
	ScrollInfo          string `toml:"scroll-info"`
	GhostText           string `toml:"ghost-text"`
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
	Core    CoreConfig    `toml:"core"`
	UI      UIConfig      `toml:"ui"`
	Git     GitConfig     `toml:"git"`
	Updater UpdaterConfig `toml:"updater"`
	AI      AIConfig      `toml:"ai"`
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
			if _, decodeErr := toml.Decode(string(data), cfg); decodeErr != nil {
				return cfg, fmt.Errorf("config: parse %s: %w", path, decodeErr)
			}
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return cfg, fmt.Errorf("config: invalid value: %w", err)
	}

	return cfg, nil
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
		{name: "scroll-info", value: p.ScrollInfo},
		{name: "ghost-text", value: p.GhostText},
	}
}

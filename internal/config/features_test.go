package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPresetsAreValidAndUseBottomPrompt(t *testing.T) {
	for _, name := range PresetNames {
		t.Run(name, func(t *testing.T) {
			cfg, err := Preset(name)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.PromptPosition != "bottom" {
				t.Fatalf("expected bottom prompt, got %q", cfg.UI.PromptPosition)
			}
			if err := Validate(cfg); err != nil {
				t.Fatalf("expected valid preset: %v", err)
			}
		})
	}
}

func TestDefaultConfigContentComesFromBalancedPreset(t *testing.T) {
	content, err := DefaultConfigContent()
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{`prompt-position = "bottom"`, `density = "balanced"`, `metrics = "when-high"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected generated config to contain %q", expected)
		}
	}
}

func TestValidateRejectsUnorderedThresholds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Chatbox.DurationFast = Duration(3 * time.Second)
	cfg.UI.Chatbox.DurationSlow = Duration(time.Second)
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "duration thresholds") {
		t.Fatalf("expected duration threshold error, got %v", err)
	}

	cfg = DefaultConfig()
	cfg.UI.Chatbox.CPUHigh = cfg.UI.Chatbox.CPUCritical
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "cpu thresholds") {
		t.Fatalf("expected CPU threshold error, got %v", err)
	}
}

func TestVersionVisibilityHonorsAllowAndDenyLists(t *testing.T) {
	chatbox := DefaultConfig().UI.Chatbox
	chatbox.VersionAllow = []string{"go"}
	chatbox.VersionDeny = []string{"rust"}
	if !VersionVisible(chatbox, "go") || VersionVisible(chatbox, "rust") || VersionVisible(chatbox, "php") {
		t.Fatal("expected allow and deny lists to constrain providers")
	}
}

func TestLoadPathPreservesVersionOneSnapshotBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[core]\nversion = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Chatbox.CompletedCommand != "snapshot" || cfg.UI.Chatbox.SnapshotMetadata != "always" {
		t.Fatalf("expected legacy snapshot behavior, got command=%q metadata=%q", cfg.UI.Chatbox.CompletedCommand, cfg.UI.Chatbox.SnapshotMetadata)
	}
}

func TestLoadPathTreatsAConfigWithoutSchemaAsAnExistingInstallation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nghost-text = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Chatbox.CompletedCommand != "snapshot" || cfg.UI.Chatbox.SnapshotMetadata != "always" {
		t.Fatalf("expected schema-less existing config to retain snapshot behavior, got command=%q metadata=%q", cfg.UI.Chatbox.CompletedCommand, cfg.UI.Chatbox.SnapshotMetadata)
	}
}

func TestLoadPathRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nunknown-setting = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPath(path); err == nil || !strings.Contains(err.Error(), "ui.unknown-setting") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestApplyVisualPolicyProvidesTerminalAndAccessiblePalettes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Palette = "terminal"
	cfg.UI.Contrast = "high"
	ApplyVisualPolicy(cfg)
	if cfg.UI.Colors.Night.Background != "0" || cfg.UI.Chatbox.Colors.ExitFailure != "1" {
		t.Fatalf("expected terminal ANSI palette, got %+v", cfg.UI.Colors.Night)
	}

	cfg = DefaultConfig()
	cfg.UI.ColorVision = "deuteranopia"
	ApplyVisualPolicy(cfg)
	if cfg.UI.Chatbox.Colors.ExitFailure != "#d55e00" || cfg.UI.Chatbox.Colors.ExitSuccess != "#56b4e9" {
		t.Fatal("expected color-vision-safe success and failure colors")
	}
}

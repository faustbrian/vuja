package root

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
)

func TestConfigPreviewIsBoundedAndNamesSelectedMode(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	preview := renderConfigPreview(cfg, "balanced", 60, "night")
	if !strings.Contains(preview, "balanced") || !strings.Contains(preview, "night palette") {
		t.Fatalf("expected preset and palette in preview, got %q", preview)
	}
	for _, line := range strings.Split(preview, "\n") {
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("preview line exceeds requested width: %q", line)
		}
	}
}

func TestConfigDiffRequiresExplicitDefaultsTarget(t *testing.T) {
	configDiffDefaults = false
	if err := ConfigDiffCmd.RunE(ConfigDiffCmd, nil); err == nil || !strings.Contains(err.Error(), "--defaults") {
		t.Fatalf("expected explicit defaults target, got %v", err)
	}
}

func TestConfigDiffRedactsProviderCredentials(t *testing.T) {
	defaults := config.DefaultConfig()
	current := config.DefaultConfig()
	current.AI.Providers = map[string]config.ProviderConfig{
		"private": {APIKey: "must-not-appear"},
	}
	diff := strings.Join(configDifferences(reflect.ValueOf(defaults), reflect.ValueOf(current), ""), "\n")
	if !strings.Contains(diff, "values redacted") || strings.Contains(diff, "must-not-appear") {
		t.Fatalf("expected provider credentials to be redacted, got %q", diff)
	}
}

func TestConfigPresetRequiresForceBeforeReplacingConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "vuja", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPresetWrite = true
	configPresetForce = false
	t.Cleanup(func() {
		configPresetWrite = false
		configPresetForce = false
	})
	if err := ConfigPresetCmd.RunE(ConfigPresetCmd, []string{"balanced"}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected overwrite protection, got %v", err)
	}
}

func TestConfigValidateReportsValidPath(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	content, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ConfigValidateCmd.SetOut(&output)
	if err := ConfigValidateCmd.RunE(ConfigValidateCmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "valid:") {
		t.Fatalf("expected validation result, got %q", output.String())
	}
}

func TestConfigDoctorInspectsConfigShellAndGeneratedHook(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LANG", "en_US.UTF-8")

	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	content, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configRoot, "vuja", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("source $HOME/.local/share/vuja/init.zsh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(home, ".local", "share", "vuja", "init.zsh")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("VUJA_CMD_START prompt-start\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	ConfigDoctorCmd.SetOut(&output)
	if err := ConfigDoctorCmd.RunE(ConfigDoctorCmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"OK config:", "OK shell:", "OK hook:", "truecolor advertised"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("expected doctor output to contain %q, got %q", expected, output.String())
		}
	}
}

func TestConfigToolsBypassManagedShellWatchdog(t *testing.T) {
	for _, args := range [][]string{{"config", "doctor"}, {"config", "preview"}, {"version"}} {
		if !directExecutionCommand(args) {
			t.Fatalf("expected %v to execute directly", args)
		}
	}
	if directExecutionCommand(nil) {
		t.Fatal("expected the interactive root command to retain the watchdog")
	}
}

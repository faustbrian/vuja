package root

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/internal/config"
)

func TestConfigCommands(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vuja-config-cmd-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	ConfigInitCmd.Run(ConfigInitCmd, []string{})

	configPath := filepath.Join(tmpDir, "vuja", "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("expected config file to be created at %s, but it was not", configPath)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("expected generated config to load: %v", err)
	}
	if loaded.UI.Chatbox.Prompt != "› " || len(loaded.UI.Chatbox.Segments()) == 0 {
		t.Fatalf("expected generated config to include managed chatbox defaults, got %+v", loaded.UI.Chatbox)
	}
	if loaded.UI.PromptPosition != "bottom" || loaded.UI.Density != "balanced" {
		t.Fatalf("expected new installs to use balanced bottom layout, got position=%q density=%q", loaded.UI.PromptPosition, loaded.UI.Density)
	}

	buf := new(bytes.Buffer)
	ConfigShowCmd.SetOut(buf)
	ConfigShowCmd.Run(ConfigShowCmd, []string{})
	if buf.Len() == 0 {
		t.Errorf("expected show command to output configuration")
	}
	if strings.Contains(buf.String(), "\nstatus = ") {
		t.Fatalf("expected resolved configuration to omit the legacy status field, got %q", buf.String())
	}
}

func TestRuntimeConfigFallsBackWhenAColorIsInvalid(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	configDir := filepath.Join(configRoot, "vuja")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[ui.colors.night]
border = "invalid"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadRuntimeConfig()
	if err == nil {
		t.Fatal("expected invalid color error")
	}
	if cfg.UI.Colors.Night.Border != "#739ee8" {
		t.Fatalf("expected safe Serein Night fallback, got %q", cfg.UI.Colors.Night.Border)
	}
}

package root

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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

	buf := new(bytes.Buffer)
	ConfigShowCmd.SetOut(buf)
	ConfigShowCmd.Run(ConfigShowCmd, []string{})
	if buf.Len() == 0 {
		t.Errorf("expected show command to output configuration")
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

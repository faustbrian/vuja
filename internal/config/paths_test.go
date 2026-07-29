package config

import (
	"path/filepath"
	"testing"
)

func TestPathsUseVujaXDGDirectories(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))

	configPath, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	cachePath, err := CachePath()
	if err != nil {
		t.Fatal(err)
	}

	if want := filepath.Join(root, "config", "vuja", "config.toml"); configPath != want {
		t.Fatalf("expected config path %q, got %q", want, configPath)
	}
	if want := filepath.Join(root, "data", "vuja", "state.toml"); statePath != want {
		t.Fatalf("expected state path %q, got %q", want, statePath)
	}
	if want := filepath.Join(root, "cache", "vuja"); cachePath != want {
		t.Fatalf("expected cache path %q, got %q", want, cachePath)
	}
}

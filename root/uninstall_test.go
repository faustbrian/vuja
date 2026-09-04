package root

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveUninstallDataPreservesConfigurationAndHistoryByDefault(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")
	writeUninstallFixture(t, configDir, "config.toml")
	writeUninstallFixture(t, stateDir, "history.db")
	writeUninstallFixture(t, cacheDir, "transient")

	if err := removeUninstallData(configDir, stateDir, cacheDir, false); err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		filepath.Join(configDir, "config.toml"),
		filepath.Join(stateDir, "history.db"),
	} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("expected uninstall to retain %s: %v", retained, err)
		}
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("expected uninstall to remove disposable cache, got %v", err)
	}
}

func TestRemoveUninstallDataPurgesConfigurationAndHistoryOnlyWhenExplicit(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")
	writeUninstallFixture(t, configDir, "config.toml")
	writeUninstallFixture(t, stateDir, "history.db")
	writeUninstallFixture(t, cacheDir, "transient")

	if err := removeUninstallData(configDir, stateDir, cacheDir, true); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{configDir, stateDir, cacheDir} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected explicit purge to remove %s, got %v", removed, err)
		}
	}
}

func TestUninstallDoesNotClaimSuccessWhenCleanupFails(t *testing.T) {
	home := t.TempDir()
	cacheParent := filepath.Join(home, "locked")
	cacheDir := filepath.Join(cacheParent, "vuja")
	writeUninstallFixture(t, cacheDir, "transient")
	if err := os.Chmod(cacheParent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cacheParent, 0700) })

	var output bytes.Buffer
	err := uninstallFromPaths(&output, home, nil, "", "", cacheDir, nil, "", false)
	if err == nil {
		t.Fatal("expected uninstall cleanup failure")
	}
	if bytes.Contains(output.Bytes(), []byte("successfully uninstalled")) {
		t.Fatalf("expected failure not to claim success, got %q", output.String())
	}
}

func writeUninstallFixture(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

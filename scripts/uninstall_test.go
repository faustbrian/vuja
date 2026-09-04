package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandaloneUninstallerPreservesDurableDataByDefault(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeStandaloneUninstallFixture(t, home, ".config/vuja/config.toml")
	writeStandaloneUninstallFixture(t, home, ".local/share/vuja/history.db")
	writeStandaloneUninstallFixture(t, home, ".cache/vuja/transient")

	output := runStandaloneUninstaller(t, home)
	for _, retained := range []string{
		filepath.Join(home, ".config/vuja/config.toml"),
		filepath.Join(home, ".local/share/vuja/history.db"),
	} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("expected standalone uninstall to retain %s: %v\n%s", retained, err, output)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cache/vuja")); !os.IsNotExist(err) {
		t.Fatalf("expected standalone uninstall to remove disposable cache, got %v\n%s", err, output)
	}
	if !strings.Contains(output, "Preserved configuration and durable history") {
		t.Fatalf("expected standalone uninstall to explain preserved data, got %s", output)
	}
}

func TestStandaloneUninstallerPurgesDurableDataOnlyWhenExplicit(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeStandaloneUninstallFixture(t, home, ".config/vuja/config.toml")
	writeStandaloneUninstallFixture(t, home, ".local/share/vuja/history.db")
	writeStandaloneUninstallFixture(t, home, ".cache/vuja/transient")

	output := runStandaloneUninstaller(t, home, "--purge")
	for _, removed := range []string{
		filepath.Join(home, ".config/vuja"),
		filepath.Join(home, ".local/share/vuja"),
		filepath.Join(home, ".cache/vuja"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected explicit standalone purge to remove %s, got %v\n%s", removed, err, output)
		}
	}
}

func TestStandaloneUninstallerDoesNotDelegateToInstalledVuja(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "delegated")
	installed := filepath.Join(binDir, "vuja")
	contents := "#!/bin/sh\nprintf delegated > '" + marker + "'\n"
	if err := os.WriteFile(installed, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	writeStandaloneUninstallFixture(t, home, ".config/vuja/config.toml")

	output, err := runStandaloneUninstallerResult(t, home, binDir+":/usr/bin:/bin")
	if err != nil {
		t.Fatalf("standalone uninstall: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected standalone uninstall not to execute an installed Vuja, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/vuja/config.toml")); err != nil {
		t.Fatalf("expected standalone fallback to preserve durable data: %v", err)
	}
}

func TestStandaloneUninstallerRemovesCodexResumeHandlers(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeStandaloneUninstallFixture(t, home, "Applications/Vuja URL Handler.app/Contents/Info.plist")
	writeStandaloneUninstallFixture(t, home, ".local/share/applications/vuja-url-handler.desktop")

	output := runStandaloneUninstaller(t, home)
	for _, removed := range []string{
		filepath.Join(home, "Applications/Vuja URL Handler.app"),
		filepath.Join(home, ".local/share/applications/vuja-url-handler.desktop"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected standalone uninstall to remove action handler %s, got %v\n%s", removed, err, output)
		}
	}
}

func TestStandaloneUninstallerRemovesDefaultHandlerWithCustomDataHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	customDataHome := filepath.Join(home, "custom-data")
	writeStandaloneUninstallFixture(t, home, ".local/share/applications/vuja-url-handler.desktop")
	writeStandaloneUninstallFixture(t, customDataHome, "applications/vuja-url-handler.desktop")

	output, err := runStandaloneUninstallerResultWithDataHome(t, home, "/usr/bin:/bin", filepath.Join(home, ".cache"), customDataHome)
	if err != nil {
		t.Fatalf("standalone uninstall: %v\n%s", err, output)
	}
	for _, removed := range []string{
		filepath.Join(home, ".local/share/applications/vuja-url-handler.desktop"),
		filepath.Join(customDataHome, "applications/vuja-url-handler.desktop"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected standalone uninstall to remove action handler %s, got %v\n%s", removed, err, output)
		}
	}
}

func TestStandaloneUninstallerReportsCleanupFailure(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheHome := filepath.Join(home, "locked-cache")
	writeStandaloneUninstallFixture(t, cacheHome, "vuja/transient")
	if err := os.Chmod(cacheHome, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cacheHome, 0700) })

	output, err := runStandaloneUninstallerResultWithCache(t, home, "/usr/bin:/bin", cacheHome)
	if err == nil {
		t.Fatalf("expected cleanup failure to return non-zero\n%s", output)
	}
	if strings.Contains(output, "successfully uninstalled") {
		t.Fatalf("expected cleanup failure not to claim success\n%s", output)
	}
}

func runStandaloneUninstaller(t *testing.T, home string, args ...string) string {
	t.Helper()
	output, err := runStandaloneUninstallerResult(t, home, "/usr/bin:/bin", args...)
	if err != nil {
		t.Fatalf("standalone uninstall: %v\n%s", err, output)
	}
	return output
}

func runStandaloneUninstallerResult(t *testing.T, home, path string, args ...string) (string, error) {
	t.Helper()
	return runStandaloneUninstallerResultWithCache(t, home, path, filepath.Join(home, ".cache"), args...)
}

func runStandaloneUninstallerResultWithCache(t *testing.T, home, path, cacheHome string, args ...string) (string, error) {
	t.Helper()
	return runStandaloneUninstallerResultWithDataHome(t, home, path, cacheHome, filepath.Join(home, ".local/share"), args...)
}

func runStandaloneUninstallerResultWithDataHome(t *testing.T, home, path, cacheHome, dataHome string, args ...string) (string, error) {
	t.Helper()

	script, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	safeSystemBinary := filepath.Join(t.TempDir(), "system", "vuja")
	script = []byte(strings.ReplaceAll(string(script), "/usr/local/bin/vuja", safeSystemBinary))
	scriptPath := filepath.Join(t.TempDir(), "uninstall.sh")
	if err := os.WriteFile(scriptPath, script, 0700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	commandArgs := append([]string{scriptPath}, args...)
	cmd := exec.CommandContext(ctx, "sh", commandArgs...)
	cmd.Env = append(installerTestEnvironment(),
		"HOME="+home,
		"PATH="+path,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+dataHome,
		"XDG_CACHE_HOME="+cacheHome,
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func writeStandaloneUninstallFixture(t *testing.T, home, relativePath string) {
	t.Helper()

	path := filepath.Join(home, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

package root

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareManagedShellInitSourcesZshConfigWithOriginalZDOTDIR(t *testing.T) {
	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}

	configDir := t.TempDir()
	confDir := filepath.Join(configDir, "conf.d")
	if err := os.Mkdir(confDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(configDir, ".zshrc"),
		[]byte("for config in $ZDOTDIR/conf.d/*.zsh(N); do source \"$config\"; done\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(confDir, "functions.zsh"),
		[]byte("alias dotdot='cd ..'\nfunction gtm() { print loaded; }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ZDOTDIR", configDir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	command := exec.CommandContext(t.Context(), zshPath, "-i", "-c", "alias dotdot >/dev/null && whence -w gtm")
	command.Env = replaceEnv(os.Environ(), "VUJA_PID", "1")
	command.Env = replaceEnv(command.Env, "VUJA_FD", "99")
	cleanup, err := prepareManagedShellInit(command, "zsh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("expected ZDOTDIR-based aliases and functions to load: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "gtm: function") {
		t.Fatalf("expected function from ZDOTDIR/conf.d to load, got %q", output)
	}
}

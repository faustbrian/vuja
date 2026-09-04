package root

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/config"
)

// prepareManagedShellInit injects Vuja's private shell protocol into the child
// shell without requiring an installed user hook. User configuration is still
// sourced first so aliases, functions, and normal shell behavior are retained.
func prepareManagedShellInit(command *exec.Cmd, shellName string) (func(), error) {
	cacheDir, err := config.CachePath()
	if err != nil {
		return nil, err
	}
	if err := config.EnsurePrivateDir(cacheDir); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(cacheDir, "managed-shell-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binaryPath, err := os.Executable()
	if err != nil {
		cleanup()
		return nil, err
	}
	hookPath := filepath.Join(dir, "vuja-hook")
	if err := config.WritePrivateFile(hookPath, []byte(shellInitScript(shellName, binaryPath))); err != nil {
		cleanup()
		return nil, err
	}

	switch shellName {
	case "zsh":
		originalDir := shell.GetZshConfigDir()
		for _, name := range []string{".zshenv", ".zprofile"} {
			content := sourceIfPresent(filepath.Join(originalDir, name)) + "\nexport ZDOTDIR=" + shellQuote(dir) + "\n"
			if err := config.WritePrivateFile(filepath.Join(dir, name), []byte(content)); err != nil {
				cleanup()
				return nil, err
			}
		}
		zshrc := sourceIfPresent(filepath.Join(originalDir, ".zshrc")) +
			"\nexport ZDOTDIR=" + shellQuote(originalDir) + "\nsource " + shellQuote(hookPath) + "\n"
		if err := config.WritePrivateFile(filepath.Join(dir, ".zshrc"), []byte(zshrc)); err != nil {
			cleanup()
			return nil, err
		}
		command.Env = replaceEnv(command.Env, "ZDOTDIR", dir)
	case "fish":
		command.Args = append(command.Args, "--init-command", "source "+shellQuote(hookPath))
	default:
		bashrc := sourceIfPresent(filepath.Join(userHome(), ".bashrc")) + "\nsource " + shellQuote(hookPath) + "\n"
		bootstrap := filepath.Join(dir, "bashrc")
		if err := config.WritePrivateFile(bootstrap, []byte(bashrc)); err != nil {
			cleanup()
			return nil, err
		}
		command.Args = append(command.Args, "--rcfile", bootstrap)
	}
	return cleanup, nil
}

func sourceIfPresent(path string) string {
	return fmt.Sprintf("[[ -r %s ]] && source %s", shellQuote(path), shellQuote(path))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func replaceEnv(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, prefix+value)
}

func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}

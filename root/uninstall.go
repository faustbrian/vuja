package root

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallPurge, "purge", false, "also remove configuration and durable history")
	rootCmd.AddCommand(uninstallCmd)
}

var uninstallPurge bool

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall Vuja and remove shell integrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		output := cmd.OutOrStdout()
		fmt.Fprintln(output, "Uninstalling Vuja...")
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("determine home directory: %w", err)
		}

		zshrcPath := filepath.Join(shell.GetZshConfigDir(), ".zshrc")

		configFiles := []string{
			zshrcPath,
			filepath.Join(home, ".bashrc"),
			filepath.Join(shell.GetFishConfigDir(), "config.fish"),
		}

		var configDir, stateDir, cacheDir string
		if cfgPath, err := config.ConfigPath(); err == nil {
			configDir = filepath.Dir(cfgPath)
		}
		if statePath, err := config.StatePath(); err == nil {
			stateDir = filepath.Dir(statePath)
		}
		if cachePath, err := config.CachePath(); err == nil {
			cacheDir = cachePath
		}

		binLocations := []string{
			filepath.Join(home, ".local", "bin", "vuja"),
			"/usr/local/bin/vuja",
		}
		if exe, err := os.Executable(); err == nil && exe != "" {
			binLocations = append(binLocations, exe)
		}
		return uninstallFromPaths(
			output,
			home,
			configFiles,
			configDir,
			stateDir,
			cacheDir,
			binLocations,
			"vuja.log",
			uninstallPurge,
		)
	},
}

func uninstallFromPaths(
	output io.Writer,
	home string,
	configFiles []string,
	configDir, stateDir, cacheDir string,
	binLocations []string,
	logPath string,
	purge bool,
) error {
	var errs []error
	if err := uninstallCodexResumeURLHandler(home); err != nil {
		errs = append(errs, fmt.Errorf("remove action handler: %w", err))
	}
	for _, file := range configFiles {
		modified, err := cleanShellConfigChecked(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("clean shell configuration %s: %w", file, err))
			continue
		}
		if modified {
			fmt.Fprintf(output, "✓ Removed integration from %s\n", file)
		}
	}
	if err := removeUninstallData(configDir, stateDir, cacheDir, purge); err != nil {
		errs = append(errs, err)
	} else if purge {
		fmt.Fprintln(output, "✓ Removed configuration, durable history, state, and cache data")
	} else {
		fmt.Fprintln(output, "✓ Removed disposable cache data")
		fmt.Fprintln(output, "✓ Preserved configuration and durable history; use --purge to remove them")
	}

	anyFound := false
	for _, location := range binLocations {
		_, err := os.Stat(location)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect binary %s: %w", location, err))
			continue
		}
		anyFound = true
		if err := os.Remove(location); err != nil {
			errs = append(errs, fmt.Errorf("remove binary %s: %w", location, err))
		} else {
			fmt.Fprintf(output, "✓ Removed binary: %s\n", location)
		}
	}
	if !anyFound {
		fmt.Fprintln(output, "✓ No leftover binary files found")
	}
	if logPath != "" {
		if err := removeIfPresent(logPath); err != nil {
			errs = append(errs, fmt.Errorf("remove legacy log: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("uninstall incomplete: %w", err)
	}

	fmt.Fprintln(output, "\n✓ Vuja has been successfully uninstalled")
	if os.Getenv("VUJA_PID") != "" {
		fmt.Fprintln(output, "\n⚠️  You are currently inside an active Vuja session.")
		fmt.Fprintln(output, "Vuja runs as the parent process of this terminal - do NOT run 'pkill vuja'")
		fmt.Fprintln(output, "as it will immediately close this terminal window.")
		fmt.Fprintln(output, "\nTo fully exit, simply close this terminal window and open a new one.")
		fmt.Fprintln(output, "Vuja will not start again since the shell config has been cleaned up.")
	} else {
		fmt.Fprintln(output, "Please close and reopen your terminal to complete the uninstall.")
	}
	return nil
}

func removeUninstallData(configDir, stateDir, cacheDir string, purge bool) error {
	paths := []string{cacheDir}
	if purge {
		paths = append(paths, configDir, stateDir)
	}
	var errs []error
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func cleanShellConfig(filePath string) bool {
	modified, _ := cleanShellConfigChecked(filePath)
	return modified
}

func cleanShellConfigChecked(filePath string) (bool, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	modified := false
	skipNext := false

	for scanner.Scan() {
		line := scanner.Text()
		if skipNext {
			skipNext = false
			modified = true
			continue
		}
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "# vuja autocomplete") ||
			strings.Contains(lowerLine, "# vuja autostart") ||
			strings.Contains(lowerLine, "# vuja prompt finalization") {
			modified = true
			skipNext = true
			continue
		}
		if strings.Contains(lowerLine, "vuja init") {
			modified = true
			continue
		}
		if strings.Contains(lowerLine, "source ") && strings.Contains(lowerLine, "vuja/init.") {
			modified = true
			continue
		}
		lines = append(lines, line)
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return false, scanErr
	}

	if !modified {
		return false, nil
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}

	output := strings.Join(lines, "\n")
	if len(lines) > 0 {
		output += "\n"
	}

	if err = os.WriteFile(filePath, []byte(output), 0644); err != nil {
		return false, err
	}
	return true, nil
}

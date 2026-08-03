package root

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(uninstallCmd)
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall Vuja and remove shell integrations",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Uninstalling Vuja...")
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Printf("! could not determine home directory: %v\n", err)
			return
		}

		zshrcPath := filepath.Join(shell.GetZshConfigDir(), ".zshrc")

		configFiles := []string{
			zshrcPath,
			filepath.Join(home, ".bashrc"),
			filepath.Join(shell.GetFishConfigDir(), "config.fish"),
		}

		for _, file := range configFiles {
			if cleanShellConfig(file) {
				fmt.Printf("✓ Removed integration from %s\n", file)
			}
		}

		// Remove config, state, and cache directories
		if cfgPath, err := config.ConfigPath(); err == nil {
			if cfgDir := filepath.Dir(cfgPath); os.RemoveAll(cfgDir) == nil {
				fmt.Printf("✓ Removed config directory: %s\n", cfgDir)
			}
		}
		if statePath, err := config.StatePath(); err == nil {
			if stateDir := filepath.Dir(statePath); os.RemoveAll(stateDir) == nil {
				fmt.Printf("✓ Removed state directory: %s\n", stateDir)
			}
		}
		if cachePath, err := config.CachePath(); err == nil {
			if os.RemoveAll(cachePath) == nil {
				fmt.Printf("✓ Removed cache directory: %s\n", cachePath)
			}
		}

		binLocations := []string{
			filepath.Join(home, ".local", "bin", "vuja"),
			"/usr/local/bin/vuja",
		}
		if exe, err := os.Executable(); err == nil && exe != "" {
			binLocations = append(binLocations, exe)
		}

		anyFound := false
		for _, loc := range binLocations {
			if _, err := os.Stat(loc); err == nil {
				anyFound = true
				if errRemove := os.Remove(loc); errRemove == nil {
					fmt.Printf("✓ Removed binary: %s\n", loc)
				} else {
					fmt.Printf("! Could not remove binary at %s (try with sudo): %v\n", loc, errRemove)
				}
			}
		}

		if !anyFound {
			fmt.Println("✓ No leftover binary files found")
		}

		_ = os.Remove("vuja.log")

		fmt.Println("\n✓ Vuja has been successfully uninstalled")
		if os.Getenv("VUJA_PID") != "" {
			fmt.Println("\n⚠️  You are currently inside an active Vuja session.")
			fmt.Println("Vuja runs as the parent process of this terminal - do NOT run 'pkill vuja'")
			fmt.Println("as it will immediately close this terminal window.")
			fmt.Println("\nTo fully exit, simply close this terminal window and open a new one.")
			fmt.Println("Vuja will not start again since the shell config has been cleaned up.")
		} else {
			fmt.Println("Please close and reopen your terminal to complete the uninstall.")
		}
	},
}

func cleanShellConfig(filePath string) bool {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false
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
		return false
	}

	if !modified {
		return false
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

	err = os.WriteFile(filePath, []byte(output), 0644)
	return err == nil
}

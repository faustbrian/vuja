package root

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

var ConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "manage vuja configuration",
}

var ConfigInitCmd = &cobra.Command{
	Use:   "init",
	Short: "initialize the canonical balanced configuration",
	Run: func(cmd *cobra.Command, args []string) {
		path, err := config.ConfigPath()
		if err != nil {
			fmt.Printf("failed to get config path: %v\n", err)
			return
		}

		if _, statErr := os.Stat(path); statErr == nil {
			fmt.Printf("config file already exists at %s\n", path)
			return
		}

		if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			fmt.Printf("failed to secure config directory: %v\n", err)
			return
		}

		content, renderErr := config.DefaultConfigContent()
		if renderErr != nil {
			fmt.Printf("failed to render config file: %v\n", renderErr)
			return
		}
		err = config.WritePrivateFile(path, content)
		if err != nil {
			fmt.Printf("failed to write config file: %v\n", err)
			return
		}
		fmt.Printf("initialized config file at %s\n", path)
	},
}

var ConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "show the resolved configuration",
	Run: func(cmd *cobra.Command, args []string) {
		enc := toml.NewEncoder(cmd.OutOrStdout())
		if err := enc.Encode(config.Get()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to encode config: %v\n", err)
		}
	},
}

func init() {
	initConfigToolFlags()
	ConfigCmd.AddCommand(ConfigInitCmd)
	ConfigCmd.AddCommand(ConfigShowCmd)
	ConfigCmd.AddCommand(ConfigValidateCmd)
	ConfigCmd.AddCommand(ConfigPresetCmd)
	ConfigCmd.AddCommand(ConfigPreviewCmd)
	ConfigCmd.AddCommand(ConfigDiffCmd)
	ConfigCmd.AddCommand(ConfigMigrateCmd)
	ConfigCmd.AddCommand(ConfigDoctorCmd)
	rootCmd.AddCommand(ConfigCmd)
}

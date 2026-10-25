package root

import (
	"fmt"
	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

var (
	configPresetWrite  bool
	configPresetForce  bool
	configMigrateWrite bool
	configDiffDefaults bool
)

var ConfigValidateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "validate configuration without starting Vuja",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := requestedConfigPath(args)
		if err != nil {
			return err
		}
		if _, err := config.LoadPath(path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "valid: %s\n", path)
		return nil
	},
}

var ConfigPresetCmd = &cobra.Command{
	Use:   "preset <minimal|balanced|context-rich|ops>",
	Short: "render or write an opinionated configuration preset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Preset(args[0])
		if err != nil {
			return err
		}
		content, err := config.Render(cfg)
		if err != nil {
			return err
		}
		if !configPresetWrite {
			_, err = cmd.OutOrStdout().Write(content)
			return err
		}
		path, err := config.ConfigPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil && !configPresetForce {
			return fmt.Errorf("config file already exists at %s (use --force to replace it)", path)
		}
		if err := writeConfigFile(path, content); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s preset to %s\n", args[0], path)
		return nil
	},
}

var ConfigPreviewCmd = newConfigPreviewCommand()

func newConfigPreviewCommand() *cobra.Command {
	var name, scenario string
	var width, height int
	var day, night bool
	command := &cobra.Command{
		Use:   "preview [path]",
		Short: "preview fictional snapshots with the production renderer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if day && night {
				return fmt.Errorf("--day and --night cannot be combined")
			}
			if width < 40 || width > 500 {
				return fmt.Errorf("--width must be between 40 and 500")
			}
			cfg, err := config.Preset(name)
			if err != nil {
				return err
			}
			label := name
			if len(args) > 0 {
				if cmd.Flags().Changed("preset") {
					return fmt.Errorf("choose a config path or --preset, not both")
				}
				_, cfg, err = readSetupConfig(args[0])
				if err != nil {
					return err
				}
				if cfg == nil {
					return fmt.Errorf("configuration does not exist")
				}
				label = "custom"
			}
			mode := "auto"
			if day {
				mode = "day"
			} else if night {
				mode = "night"
			}
			preview, err := renderConfigScenario(cfg, label, width, height, mode, scenario)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), preview)
			return err
		},
	}
	command.Flags().StringVar(&name, "preset", "balanced", "preset to preview")
	command.Flags().IntVar(&width, "width", 120, "preview width")
	command.Flags().IntVar(&height, "height", 24, "preview height (8-100)")
	command.Flags().StringVar(&scenario, "scenario", "clean", "clean|conflicts|multiline|running|history")
	command.Flags().BoolVar(&day, "day", false, "preview the day palette")
	command.Flags().BoolVar(&night, "night", false, "preview the night palette")
	return command
}

var ConfigDiffCmd = &cobra.Command{
	Use:   "diff [path]",
	Short: "show values that differ from the balanced defaults",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !configDiffDefaults {
			return fmt.Errorf("--defaults is required")
		}
		path, err := requestedConfigPath(args)
		if err != nil {
			return err
		}
		current, err := config.LoadPath(path)
		if err != nil {
			return err
		}
		defaults, err := config.Preset("balanced")
		if err != nil {
			return err
		}
		changes := configDifferences(reflect.ValueOf(defaults), reflect.ValueOf(current), "")
		if len(changes) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no differences")
			return nil
		}
		for _, change := range changes {
			fmt.Fprintln(cmd.OutOrStdout(), change)
		}
		return nil
	},
}

var ConfigMigrateCmd = &cobra.Command{
	Use:   "migrate [path]",
	Short: "resolve legacy and missing fields into the current schema",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := requestedConfigPath(args)
		if err != nil {
			return err
		}
		cfg, err := config.LoadPath(path)
		if err != nil {
			return err
		}
		cfg.Core.Version = config.CurrentVersion
		content, err := config.Render(cfg)
		if err != nil {
			return err
		}
		if !configMigrateWrite {
			_, err = cmd.OutOrStdout().Write(content)
			return err
		}
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		backup := path + ".bak." + time.Now().Format("20060102-150405")
		if err := config.WritePrivateFile(backup, original); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
		if err := writeConfigFile(path, content); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "migrated %s (backup: %s)\n", path, backup)
		return nil
	},
}

var ConfigDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "inspect configuration and shell integration without starting Vuja",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runConfigDoctor(cmd)
	},
}

func initConfigToolFlags() {
	ConfigPresetCmd.Flags().BoolVar(&configPresetWrite, "write", false, "write the preset to the config path")
	ConfigPresetCmd.Flags().BoolVar(&configPresetForce, "force", false, "replace an existing config when writing")
	ConfigMigrateCmd.Flags().BoolVar(&configMigrateWrite, "write", false, "write the migrated config and create a backup")
	ConfigDiffCmd.Flags().BoolVar(&configDiffDefaults, "defaults", false, "compare with balanced defaults")
}

func requestedConfigPath(args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Clean(args[0]), nil
	}
	return config.ConfigPath()
}

func writeConfigFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	canonical, canonicalErr := config.ConfigPath()
	if canonicalErr == nil && filepath.Clean(path) == filepath.Clean(canonical) {
		if err := config.EnsurePrivateDir(directory); err != nil {
			return err
		}
	} else if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".config-*.toml")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(config.PrivateFileMode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return config.RestrictPrivateFiles(path)
}

func configDifferences(defaults, current reflect.Value, prefix string) []string {
	if defaults.Kind() == reflect.Pointer {
		defaults, current = defaults.Elem(), current.Elem()
	}
	if defaults.Kind() != reflect.Struct {
		if !reflect.DeepEqual(defaults.Interface(), current.Interface()) {
			if prefix == "ai.providers" {
				return []string{"ai.providers: differs (values redacted)"}
			}
			return []string{fmt.Sprintf("%s: %v -> %v", prefix, defaults.Interface(), current.Interface())}
		}
		return nil
	}
	var result []string
	typeInfo := defaults.Type()
	for index := 0; index < defaults.NumField(); index++ {
		field := typeInfo.Field(index)
		name := strings.Split(field.Tag.Get("toml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		result = append(result, configDifferences(defaults.Field(index), current.Field(index), path)...)
	}
	sort.Strings(result)
	return result
}

func runConfigDoctor(cmd *cobra.Command) error {
	doctor := inspectDoctor()
	path, cfg, configErr := doctor.configPath, doctor.cfg, doctor.configErr
	if configErr != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "FAIL config: %v\n", configErr)
		cfg = config.DefaultConfig()
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "OK config: %s (schema %d)\n", path, cfg.Core.Version)
	}
	if cfg.Core.Version < config.CurrentVersion {
		fmt.Fprintf(cmd.OutOrStdout(), "WARN config: schema %d can be migrated to %d\n", cfg.Core.Version, config.CurrentVersion)
	}

	rcPath := doctor.startupPath
	fmt.Fprintln(cmd.OutOrStdout(), "OK shell: managed Vuja protocol is built in")
	if !doctor.startupAvailable {
		fmt.Fprintf(cmd.OutOrStdout(), "INFO shell: optional startup file unavailable at %s\n", rcPath)
	} else {
		if doctor.autostart {
			fmt.Fprintf(cmd.OutOrStdout(), "OK shell: optional autostart referenced by %s\n", rcPath)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "INFO shell: no optional Vuja autostart hook")
		}
		if len(doctor.conflicts) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "WARN prompt conflicts: %s\n", strings.Join(doctor.conflicts, ", "))
		}
	}

	hook := doctor.hookPath
	if doctor.hookState != "unavailable" {
		if doctor.hookState == "current" {
			fmt.Fprintf(cmd.OutOrStdout(), "OK optional hook: %s\n", hook)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "WARN optional hook: generated integration is stale at %s\n", hook)
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "INFO optional hook: not installed")
	}
	termName := doctor.term
	if !doctor.interactive {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: TERM does not advertise an interactive terminal")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "OK terminal: TERM=%s\n", termName)
	}
	if doctor.truecolor {
		fmt.Fprintln(cmd.OutOrStdout(), "OK terminal: truecolor advertised")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: truecolor not advertised; palette=terminal is safest")
	}
	if !doctor.utf8 {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: UTF-8 locale not detected")
	}
	if doctor.columns > 0 && doctor.columns < 40 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARN terminal: narrow width %d\n", doctor.columns)
	}
	return configErr
}

func activeShellConfiguration(content string) string {
	lines := strings.Split(content, "\n")
	active := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		active = append(active, line)
	}
	return strings.Join(active, "\n")
}

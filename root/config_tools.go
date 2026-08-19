package root

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

var (
	configPresetWrite  bool
	configPresetForce  bool
	configPreviewName  string
	configPreviewWidth int
	configPreviewDay   bool
	configPreviewNight bool
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

var ConfigPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "preview a preset at a terminal width without starting Vuja",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if configPreviewDay && configPreviewNight {
			return fmt.Errorf("--day and --night cannot be combined")
		}
		if configPreviewWidth < 40 || configPreviewWidth > 500 {
			return fmt.Errorf("--width must be between 40 and 500")
		}
		cfg, err := config.Preset(configPreviewName)
		if err != nil {
			return err
		}
		mode := "auto"
		if configPreviewDay {
			mode = "day"
		} else if configPreviewNight {
			mode = "night"
		}
		fmt.Fprint(cmd.OutOrStdout(), renderConfigPreview(cfg, configPreviewName, configPreviewWidth, mode))
		return nil
	},
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
	ConfigPreviewCmd.Flags().StringVar(&configPreviewName, "preset", "balanced", "preset to preview")
	ConfigPreviewCmd.Flags().IntVar(&configPreviewWidth, "width", 120, "preview width")
	ConfigPreviewCmd.Flags().BoolVar(&configPreviewDay, "day", false, "preview the day palette")
	ConfigPreviewCmd.Flags().BoolVar(&configPreviewNight, "night", false, "preview the night palette")
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
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
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

func renderConfigPreview(cfg *config.Config, preset string, width int, mode string) string {
	width = max(width, 40)
	inner := width - 4
	clip := func(value string) string {
		for utf8.RuneCountInString(value) > inner {
			_, size := utf8.DecodeLastRuneInString(value)
			value = value[:len(value)-size]
		}
		return value
	}
	title := "~/Developer/vuja"
	if slices.Contains(cfg.UI.Chatbox.TitleRight, "versions") {
		title += "                                      Go 1.26.0"
	}
	left := previewStatusValues(cfg.UI.Chatbox.StatusLeft)
	right := previewStatusValues(cfg.UI.Chatbox.StatusRight)
	status := strings.Join(left, cfg.UI.Chatbox.Separator)
	if len(right) > 0 {
		status += "                                      " + strings.Join(right, cfg.UI.Chatbox.Separator)
	}
	header := clip(fmt.Sprintf("preset %s · %s density · %s palette · width %d", preset, cfg.UI.Density, mode, width))
	palette := cfg.UI.Colors.Night
	if mode == "day" {
		palette = cfg.UI.Colors.Day
	}
	paint := func(foreground, background, value string) string {
		return terminalTrueColor("48", background) + terminalTrueColor("38", foreground) + value + "\x1b[0m"
	}
	pad := func(value string) string {
		value = clip(value)
		return value + strings.Repeat(" ", max(inner-ansi.StringWidth(value), 0))
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s\n",
		header,
		paint(cfg.UI.Chatbox.Colors.Directory, palette.StatusBackground, pad(title)),
		paint(palette.Text, palette.SurfaceBackground, pad("")),
		paint(palette.Text, palette.SurfaceBackground, pad("  "+cfg.UI.Chatbox.Prompt+"git status")),
		paint(palette.Text, palette.SurfaceBackground, pad("")),
		paint(palette.StatusText, palette.StatusBackground, pad(status)),
	)
}

func previewStatusValues(segments []string) []string {
	values := make([]string, 0, len(segments)+2)
	for _, segment := range segments {
		switch segment {
		case "session":
			values = append(values, "SSH staging")
		case "git-branch":
			values = append(values, "main")
		case "git-status":
			values = append(values, "modified 2", "untracked 1")
		case "contexts":
			values = append(values, "Kube staging/apps")
		case "environment":
			values = append(values, "mise")
		case "stale":
			values = append(values, "stale git")
		case "jobs":
			values = append(values, "jobs 1")
		case "duration":
			values = append(values, "84ms")
		case "exit":
			values = append(values, "exit 0")
		case "cpu":
			values = append(values, "CPU 42%")
		case "memory":
			values = append(values, "RAM 58%")
		}
	}
	return values
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
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	cfg, configErr := config.LoadPath(path)
	if configErr != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "FAIL config: %v\n", configErr)
		cfg = config.DefaultConfig()
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "OK config: %s (schema %d)\n", path, cfg.Core.Version)
	}
	if cfg.Core.Version < config.CurrentVersion {
		fmt.Fprintf(cmd.OutOrStdout(), "WARN config: schema %d can be migrated to %d\n", cfg.Core.Version, config.CurrentVersion)
	}

	shell := cfg.Core.Shell
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
	}
	home, _ := os.UserHomeDir()
	rcNames := map[string]string{"zsh": ".zshrc", "bash": ".bashrc", "fish": filepath.Join(".config", "fish", "config.fish")}
	rcPath := filepath.Join(home, rcNames[shell])
	data, readErr := os.ReadFile(rcPath)
	if readErr != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "WARN shell: cannot read %s\n", rcPath)
	} else {
		text := activeShellConfiguration(string(data))
		if strings.Contains(text, ".local/share/vuja/init.") {
			fmt.Fprintf(cmd.OutOrStdout(), "OK shell: integration referenced by %s\n", rcPath)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "FAIL shell: integration missing from %s\n", rcPath)
		}
		var conflicts []string
		for name, marker := range map[string]string{"starship": "starship init", "powerlevel10k": "powerlevel10k", "oh-my-posh": "oh-my-posh", "spaceship": "spaceship", "pure": "prompt pure"} {
			if strings.Contains(strings.ToLower(text), marker) {
				conflicts = append(conflicts, name)
			}
		}
		sort.Strings(conflicts)
		if len(conflicts) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "WARN prompt conflicts: %s\n", strings.Join(conflicts, ", "))
		}
	}

	hook := filepath.Join(home, ".local", "share", "vuja", "init."+shell)
	if hookData, hookErr := os.ReadFile(hook); hookErr == nil && len(hookData) > 0 {
		hookText := string(hookData)
		if strings.Contains(hookText, "VUJA_CMD_START") && strings.Contains(hookText, "prompt-start") {
			fmt.Fprintf(cmd.OutOrStdout(), "OK hook: %s\n", hook)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "FAIL hook: generated integration is stale at %s\n", hook)
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "FAIL hook: missing %s\n", hook)
	}
	termName := os.Getenv("TERM")
	if termName == "" || termName == "dumb" {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: TERM does not advertise an interactive terminal")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "OK terminal: TERM=%s\n", termName)
	}
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
	if strings.Contains(colorTerm, "truecolor") || strings.Contains(colorTerm, "24bit") {
		fmt.Fprintln(cmd.OutOrStdout(), "OK terminal: truecolor advertised")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: truecolor not advertised; palette=terminal is safest")
	}
	locale := strings.ToLower(os.Getenv("LC_ALL") + " " + os.Getenv("LC_CTYPE") + " " + os.Getenv("LANG"))
	if !strings.Contains(locale, "utf-8") && !strings.Contains(locale, "utf8") {
		fmt.Fprintln(cmd.OutOrStdout(), "WARN terminal: UTF-8 locale not detected")
	}
	if columns := os.Getenv("COLUMNS"); columns != "" {
		if value, parseErr := strconv.Atoi(columns); parseErr == nil && value < 40 {
			fmt.Fprintf(cmd.OutOrStdout(), "WARN terminal: narrow width %d\n", value)
		}
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

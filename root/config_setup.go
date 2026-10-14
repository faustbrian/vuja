package root

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

var ConfigSetupCmd = newConfigSetupCommand()

func newConfigSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup [path]",
		Short: "preview a few visual choices and confirm before saving",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := requestedConfigPath(args)
			if err != nil {
				return err
			}
			err = runConfigSetup(cmd.InOrStdin(), cmd.OutOrStdout(), path)
			if errors.Is(err, io.EOF) {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled; configuration unchanged.")
			}
			return err
		},
	}
}

// Decode original bytes without permission repair or runtime initialization.
// Do not resolve API keys, read auth.json, run providers, or source any shell.
func readSetupConfig(path string) ([]byte, *config.Config, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, nil, fmt.Errorf("configuration must be a regular file of at most 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 1024*1024 {
		return nil, nil, fmt.Errorf("configuration exceeds 1 MiB")
	}
	cfg, err := config.Decode(data)
	return data, cfg, err
}

func runConfigSetup(input io.Reader, output io.Writer, path string) error {
	original, current, err := readSetupConfig(path)
	if err != nil {
		return err
	}
	existed := current != nil
	if !existed {
		current, err = config.Preset("balanced")
		if err != nil {
			return err
		}
	}
	proposed := *current
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 4096)
	choose := func(label string, choices []string, fallback string) (string, error) {
		if _, err := fmt.Fprintf(output, "%s [%s] (blank: %s): ", label, strings.Join(choices, "|"), fallback); err != nil {
			return "", err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			value = fallback
		}
		if !slices.Contains(choices, value) {
			return "", fmt.Errorf("invalid %s choice (want: %s)", label, strings.Join(choices, "|"))
		}
		return value, nil
	}
	fallback := "keep"
	if !existed {
		fallback = "balanced"
	}
	preset, err := choose("Preset", []string{"keep", "minimal", "balanced", "context-rich", "ops"}, fallback)
	if err != nil {
		return err
	}
	if preset != "keep" {
		selected, err := config.Preset(preset)
		if err != nil {
			return err
		}
		applySetupPreset(&proposed, selected)
	}
	for _, choice := range []struct {
		label  string
		values []string
		target *string
	}{
		{"Prompt position", []string{"keep", "bottom", "classic"}, &proposed.UI.PromptPosition},
		{"Input background", []string{"keep", "full-width", "content-width"}, &proposed.UI.Chatbox.SurfaceWidth},
		{"Output scrolling", []string{"keep", "pinned", "terminal"}, &proposed.UI.Chatbox.OutputViewport},
		{"Palette", []string{"keep", "auto", "serein-day", "serein-night", "terminal"}, &proposed.UI.Palette},
	} {
		value, err := choose(choice.label, choice.values, "keep")
		if err != nil {
			return err
		}
		if value != "keep" {
			*choice.target = value
		}
	}
	if err := config.Validate(&proposed); err != nil {
		return err
	}
	preview, err := renderConfigScenario(&proposed, "proposed", 80, 24, "auto", "clean")
	if err != nil {
		return err
	}
	diff := configDifferences(reflect.ValueOf(current), reflect.ValueOf(&proposed), "")
	if _, err := fmt.Fprintln(output, "\nProposed changes:"); err != nil {
		return err
	}
	if !existed {
		_, err = fmt.Fprintln(output, "Create a new configuration.")
	}
	for _, line := range diff {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(output, preview); err != nil {
		return err
	}
	confirmation, err := choose("Save", []string{"yes", "no"}, "no")
	if err != nil {
		return err
	}
	if confirmation != "yes" {
		_, err = fmt.Fprintln(output, "Cancelled; configuration unchanged.")
		return err
	}
	if existed && len(diff) == 0 {
		_, err = fmt.Fprintln(output, "No changes; configuration unchanged.")
		return err
	}
	content, err := config.Render(&proposed)
	if err != nil {
		return err
	}
	// Recheck immediately before backup/write; a proposal must not silently
	// replace changes made while the user was choosing or reading the preview.
	latest, latestCfg, err := readSetupConfig(path)
	if err != nil {
		return err
	}
	if (latestCfg != nil) != existed || !bytes.Equal(latest, original) {
		return fmt.Errorf("configuration changed during setup; nothing saved, rerun setup")
	}
	backup := ""
	if existed {
		backup, err = backupSetupConfig(path, original)
		if err != nil {
			return err
		}
	}
	if err := writeConfigFile(path, content); err != nil {
		return fmt.Errorf("save configuration (original backup: %s): %w", backup, err)
	}
	if backup != "" {
		_, err = fmt.Fprintf(output, "Saved configuration (backup: %s).\n", backup)
	} else {
		_, err = fmt.Fprintln(output, "Saved configuration.")
	}
	return err
}

// Presets change only the finite density/segment/provider-visibility choices.
// Preserve prompt text, colors, keys, privacy/retention, credentials and all
// unrelated settings rather than replacing the existing config with a preset.
func applySetupPreset(target, selected *config.Config) {
	target.UI.Density = selected.UI.Density
	target.UI.Responsive = selected.UI.Responsive
	target.UI.MaxSuggestions = selected.UI.MaxSuggestions
	a, b := &target.UI.Chatbox, selected.UI.Chatbox
	a.TitleLeft, a.TitleCenter, a.TitleRight = b.TitleLeft, b.TitleCenter, b.TitleRight
	a.StatusLeft, a.StatusCenter, a.StatusRight = b.StatusLeft, b.StatusCenter, b.StatusRight
	a.Metrics, a.Versions = b.Metrics, b.Versions
	a.DockerContext, a.KubernetesContext, a.AWSContext = b.DockerContext, b.KubernetesContext, b.AWSContext
}

func backupSetupConfig(path string, original []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".bak.*")
	if err != nil {
		return "", fmt.Errorf("create original backup: %w", err)
	}
	name := file.Name()
	if _, err := file.Write(original); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

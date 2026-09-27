package root

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
)

func TestConfigPreviewIsBoundedAndNamesSelectedMode(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	preview := renderConfigPreview(cfg, "balanced", 60, "night")
	if !strings.Contains(preview, "balanced") || !strings.Contains(preview, "night palette") {
		t.Fatalf("expected preset and palette in preview, got %q", preview)
	}
	for _, line := range strings.Split(preview, "\n") {
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("preview line exceeds requested width: %q", line)
		}
	}
}

func TestConfigPreviewUsesTerminalBackgroundForActiveBars(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(renderConfigPreview(cfg, "balanced", 60, "night"), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected six preview rows, got %d: %q", len(lines), lines)
	}
	for _, row := range []int{1, 5} {
		if !strings.HasPrefix(lines[row], "\x1b[49m") {
			t.Fatalf("expected preview bar row %d to use terminal background, got %q", row, lines[row])
		}
	}
	for _, row := range []int{2, 3, 4} {
		if !strings.Contains(lines[row], "\x1b[48;2;36;37;40m") {
			t.Fatalf("expected preview chatbox row %d to retain the night surface, got %q", row, lines[row])
		}
	}
}

func TestConfigPreviewMatchesConfiguredSurfaceWidth(t *testing.T) {
	for _, test := range []struct {
		name           string
		mode           string
		width          int
		wantSurfaceEnd int
	}{
		{name: "full width at minimum preview width", mode: "full-width", width: 40, wantSurfaceEnd: 39},
		{name: "full width at ordinary preview width", mode: "full-width", width: 60, wantSurfaceEnd: 59},
		{name: "content width at minimum preview width", mode: "content-width", width: 40, wantSurfaceEnd: 15},
		{name: "content width at ordinary preview width", mode: "content-width", width: 60, wantSurfaceEnd: 15},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := config.Preset("balanced")
			if err != nil {
				t.Fatal(err)
			}
			cfg.UI.Chatbox.SurfaceWidth = test.mode
			lines := strings.Split(strings.TrimSuffix(renderConfigPreview(cfg, "balanced", test.width, "night"), "\n"), "\n")
			if len(lines) != 6 {
				t.Fatalf("expected six preview rows, got %d", len(lines))
			}
			screen := applyTerminalOutput(t, []byte(strings.Join(lines[1:], "\r\n")), test.width, 5)
			for column := 0; column < test.width; column++ {
				cell := screen.CellAt(column, 2)
				hasSurface := cell != nil && cell.Style.Bg != nil
				wantSurface := column <= test.wantSurfaceEnd
				if hasSurface != wantSurface {
					t.Fatalf("column %d surface=%v, want %v for %s", column, hasSurface, wantSurface, test.mode)
				}
			}
			for _, row := range []int{0, 4} {
				for column := 0; column < test.width; column++ {
					if cell := screen.CellAt(column, row); cell != nil && cell.Style.Bg != nil {
						t.Fatalf("expected active bar row %d to retain terminal background at column %d", row, column)
					}
				}
			}
		})
	}
}

func TestConfigPreviewClipsWidePromptByTerminalCellWidth(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	cfg.UI.Chatbox.Prompt = strings.Repeat("界", 20)
	cfg.UI.Chatbox.SurfaceWidth = "content-width"

	lines := strings.Split(strings.TrimSuffix(renderConfigPreview(cfg, "balanced", 40, "night"), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected six preview rows, got %d", len(lines))
	}
	if width := ansi.StringWidth(lines[3]); width != 40 {
		t.Fatalf("expected wide prompt preview row to be capped at 40 terminal cells, got %d", width)
	}
	screen := applyTerminalOutput(t, []byte(strings.Join(lines[1:], "\r\n")), 40, 5)
	for _, column := range []int{0, 39} {
		if cell := screen.CellAt(column, 2); cell == nil || cell.Style.Bg == nil {
			t.Fatalf("expected capped content-width surface boundary at column %d, got %#v", column, cell)
		}
	}
}

func TestConfigDiffRequiresExplicitDefaultsTarget(t *testing.T) {
	configDiffDefaults = false
	if err := ConfigDiffCmd.RunE(ConfigDiffCmd, nil); err == nil || !strings.Contains(err.Error(), "--defaults") {
		t.Fatalf("expected explicit defaults target, got %v", err)
	}
}

func TestConfigDiffRedactsProviderCredentials(t *testing.T) {
	defaults := config.DefaultConfig()
	current := config.DefaultConfig()
	current.AI.Providers = map[string]config.ProviderConfig{
		"private": {APIKey: "must-not-appear"},
	}
	diff := strings.Join(configDifferences(reflect.ValueOf(defaults), reflect.ValueOf(current), ""), "\n")
	if !strings.Contains(diff, "values redacted") || strings.Contains(diff, "must-not-appear") {
		t.Fatalf("expected provider credentials to be redacted, got %q", diff)
	}
}

func TestConfigPresetRequiresForceBeforeReplacingConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "vuja", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPresetWrite = true
	configPresetForce = false
	t.Cleanup(func() {
		configPresetWrite = false
		configPresetForce = false
	})
	if err := ConfigPresetCmd.RunE(ConfigPresetCmd, []string{"balanced"}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected overwrite protection, got %v", err)
	}
}

func TestConfigValidateReportsValidPath(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	content, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ConfigValidateCmd.SetOut(&output)
	if err := ConfigValidateCmd.RunE(ConfigValidateCmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "valid:") {
		t.Fatalf("expected validation result, got %q", output.String())
	}
}

func TestConfigDoctorInspectsConfigShellAndGeneratedHook(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LANG", "en_US.UTF-8")

	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	content, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configRoot, "vuja", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("source $HOME/.local/share/vuja/init.zsh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(home, ".local", "share", "vuja", "init.zsh")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("VUJA_CMD_START prompt-start\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	ConfigDoctorCmd.SetOut(&output)
	if err := ConfigDoctorCmd.RunE(ConfigDoctorCmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"OK config:", "OK shell:", "OK optional hook:", "truecolor advertised"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("expected doctor output to contain %q, got %q", expected, output.String())
		}
	}
}

func TestConfigToolsBypassManagedShellWatchdog(t *testing.T) {
	for _, args := range [][]string{{"config", "doctor"}, {"config", "preview"}, {"version"}} {
		if !directExecutionCommand(args) {
			t.Fatalf("expected %v to execute directly", args)
		}
	}
	if directExecutionCommand(nil) {
		t.Fatal("expected the interactive root command to retain the watchdog")
	}
}

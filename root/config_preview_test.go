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

func previewForTest(t *testing.T, cfg *config.Config, label string, width int, mode string) string {
	t.Helper()
	preview, err := renderConfigScenario(cfg, label, width, 24, mode, "clean")
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestConfigPreviewScenariosUseRealSurfaceGeometryWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "auth"))
	if err := os.MkdirAll(filepath.Join(home, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth", "auth.json"), []byte("fixture-auth-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		scenario  string
		width     int
		want      string
		inputRows int
	}{
		{"clean", 40, "clean", 1}, {"conflicts", 120, "conflicts 2", 1},
		{"multiline", 80, "second 界 line", 3}, {"running", 120, "Running · 12s", 1},
		{"history", 120, "all checks passed", 1},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			cfg, err := config.Preset("balanced")
			if err != nil {
				t.Fatal(err)
			}
			cfg.UI.Chatbox.CodexAccount = false
			cfg.AI.Providers = map[string]config.ProviderConfig{"private": {APIKey: "fixture-provider-secret"}}
			before, _ := config.Render(cfg)
			preview, err := renderConfigScenario(cfg, "fixture", test.width, 24, "night", test.scenario)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := config.Render(cfg)
			if !bytes.Equal(before, after) {
				t.Fatal("preview mutated caller configuration")
			}
			if strings.Contains(preview, "fixture-auth-secret") || strings.Contains(preview, "fixture-provider-secret") || strings.Contains(preview, home) {
				t.Fatal("preview exposed private data")
			}
			lines := strings.Split(strings.TrimSuffix(preview, "\n"), "\n")[1:]
			screen := applyTerminalOutput(t, []byte(strings.Join(lines, "\r\n")), test.width, len(lines))
			if !terminalContainsLine(screen, test.want) {
				t.Fatalf("scenario missing %q: %q", test.want, terminalScreenLines(screen))
			}
			// Independently measured padding/input/footer geometry, not just a
			// string parity assertion against another call to the renderer.
			coloredRows := 0
			for y := range len(lines) {
				if cell := screen.CellAt(0, y); cell != nil && cell.Style.Bg != nil {
					coloredRows++
					if last := screen.CellAt(test.width-1, y); last == nil || last.Style.Bg == nil {
						t.Fatalf("full-width surface stopped before final column in row %d", y)
					}
				}
				if ansi.StringWidth(lines[y]) > test.width {
					t.Fatal("rendered line exceeds requested width")
				}
			}
			if coloredRows != test.inputRows+2 {
				t.Fatalf("wrong input surface height: got %d, want %d", coloredRows, test.inputRows+2)
			}
		})
	}
}

func TestConfigPreviewVisualPoliciesAndDensityReachRenderer(t *testing.T) {
	cfg, _ := config.Preset("context-rich")
	cfg.UI.Chatbox.CodexAccount = false
	cfg.UI.Palette = "terminal"
	original := cfg.UI.Colors
	preview, err := renderConfigScenario(cfg, "fixture", 80, 24, "auto", "clean")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "\x1b[100m") || !reflect.DeepEqual(original, cfg.UI.Colors) {
		t.Fatal("ANSI palette not applied, or caller colors mutated")
	}
	cfg.UI.Palette = "serein-day"
	preview, err = renderConfigScenario(cfg, "fixture", 80, 24, "auto", "clean")
	if err != nil || !strings.Contains(preview, "day palette") || !strings.Contains(preview, "48;2;233;228;222") {
		t.Fatalf("named day palette not honored: %v", err)
	}
	cfg.UI.Density = "compact"
	preview, err = renderConfigScenario(cfg, "fixture", 80, 24, "night", "clean")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ansi.Strip(preview), "Go 1.26.0") || strings.Contains(ansi.Strip(preview), "mise") {
		t.Fatal("compact preview ignored semantic density priorities")
	}
}

func TestConfigPreviewRejectsUnsupportedScenariosAndDimensions(t *testing.T) {
	cfg, _ := config.Preset("balanced")
	for _, test := range []struct {
		width, height int
		scenario      string
	}{
		{39, 24, "clean"}, {501, 24, "clean"}, {80, 7, "clean"}, {80, 101, "clean"}, {80, 24, "unknown"},
	} {
		if _, err := renderConfigScenario(cfg, "fixture", test.width, test.height, "night", test.scenario); err == nil {
			t.Fatalf("invalid preview accepted: %+v", test)
		}
	}
}

func TestConfigPreviewReadsCustomConfigurationWithoutMutation(t *testing.T) {
	cfg, _ := config.Preset("balanced")
	cfg.UI.Chatbox.CodexAccount = false
	cfg.UI.Chatbox.TitleLeft = []string{"git-branch"}
	cfg.UI.Chatbox.TitleRight = nil
	cfg.UI.Chatbox.StatusLeft = []string{"git-status", "environment"}
	content, _ := config.Render(cfg)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, content, 0o640); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ConfigPreviewCmd.SetOut(&output)
	t.Cleanup(func() { ConfigPreviewCmd.SetOut(nil) })
	if err := ConfigPreviewCmd.RunE(ConfigPreviewCmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !bytes.Equal(content, after) || info.Mode().Perm() != 0o640 {
		t.Fatal("preview changed configuration bytes or permissions")
	}
	lines := strings.Split(ansi.Strip(output.String()), "\n")
	if !strings.HasPrefix(lines[1], "  main") {
		t.Fatal("command ignored custom configuration")
	}
}

// Scenario 4: layout choices must reach the same region renderer used by the
// prompt, rather than being approximated by a second preview layout.
func TestConfigPreviewHonorsRegionPlacement(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	cfg.UI.Chatbox.CodexAccount = false
	cfg.UI.Chatbox.TitleLeft = []string{"git-branch"}
	cfg.UI.Chatbox.TitleRight = nil
	cfg.UI.Chatbox.StatusLeft = nil
	cfg.UI.Chatbox.StatusCenter = []string{"directory"}
	cfg.UI.Chatbox.StatusRight = nil
	lines := strings.Split(strings.TrimSuffix(previewForTest(t, cfg, "custom", 80, "night"), "\n"), "\n")
	if got := ansi.Strip(lines[1]); !strings.HasPrefix(got, "  main") || strings.Contains(got, "Developer") {
		t.Fatalf("title does not honor configured left region: %q", got)
	}
	footer := ansi.Strip(lines[len(lines)-1])
	start := strings.Index(footer, "~/Developer/vuja")
	if start != (80-len("~/Developer/vuja"))/2 {
		t.Fatalf("directory should be centered in status region, got column %d: %q", start, footer)
	}
}

func TestConfigPreviewClassicDoesNotInventManagedBars(t *testing.T) {
	cfg, err := config.Preset("balanced")
	if err != nil {
		t.Fatal(err)
	}
	cfg.UI.PromptPosition = "classic"
	preview := ansi.Strip(previewForTest(t, cfg, "custom", 80, "night"))
	if strings.Contains(preview, "Codex ") || strings.Contains(preview, "exit 0") || strings.Contains(preview, "Go 1.") {
		t.Fatalf("classic shell-owned prompt must not invent managed bars: %q", preview)
	}
	if !strings.Contains(preview, "git status") || !strings.Contains(preview, "shell-owned") {
		t.Fatalf("classic preview must disclose shell-owned prompt geometry: %q", preview)
	}
}

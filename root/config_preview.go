package root

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/faustbrian/vuja/internal/config"
)

// All inputs are fictional. This is the production compositor, with an
// in-memory sink and no status engine, shell, recovery store or credentials.
// Flattening its rendered screen emits only cell styles, not mode changes,
// mouse tracking, alternate-screen switches or cursor movement to the caller.
func renderConfigScenario(cfg *config.Config, label string, width, height int, mode, scenario string) (string, error) {
	if width < 40 || width > 500 || height < 8 || height > 100 {
		return "", fmt.Errorf("preview dimensions must be width 40-500 and height 8-100")
	}
	if cfg == nil {
		return "", fmt.Errorf("configuration is required")
	}
	if err := config.Validate(cfg); err != nil {
		return "", err
	}
	switch scenario {
	case "clean", "conflicts", "multiline", "running", "history":
	default:
		return "", fmt.Errorf("unknown scenario (want: clean|conflicts|multiline|running|history)")
	}
	// Visual policy mutates scalar color fields; copy first so preview leaves
	// the caller's pending configuration and its proposed diff unchanged.
	visual := *cfg
	config.ApplyVisualPolicy(&visual)
	if mode == "auto" {
		mode = "night"
		if visual.UI.Palette == "serein-day" {
			mode = "day"
		}
	}
	if mode != "day" && mode != "night" {
		return "", fmt.Errorf("preview mode must be auto, day or night")
	}
	palette := visual.UI.Colors.Night
	if mode == "day" {
		palette = visual.UI.Colors.Day
	}
	header := ansi.Truncate(fmt.Sprintf("preset %s · %s density · %s palette · width %d · %s", label, visual.UI.Density, mode, width, scenario), width, "…")
	input := "git status"
	if scenario == "multiline" {
		input = "printf '%s\\n' \\\r\n  'first line' \\\r\n  'second 界 line'"
	}
	if scenario == "running" {
		input = "git push"
	}
	if visual.UI.PromptPosition == "classic" {
		// Classic's prompt belongs to shell configuration, not ChatboxConfig.
		return header + "\n" + ansi.Truncate("Classic: shell-owned prompt; illustrative input only", width, "…") + "\n" + input + "\n", nil
	}
	var output bytes.Buffer
	const marker = "fictional-preview"
	c := newTerminalCompositor(&output, "bottom", marker, width, height)
	defer c.Close()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.SetInputBoxTheme(terminalInputBoxTheme{
		Background: palette.Background, SurfaceBackground: palette.SurfaceBackground,
		CompletedSurfaceBackground: palette.CompletedSurfaceBackground, StatusBackground: palette.StatusBackground,
		StatusText: palette.StatusText, Border: palette.Border, Accent: palette.Accent, Muted: palette.Muted,
	})
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(&visual))
	exit := 0
	snapshot := statusSnapshot{
		Directory: "~/Developer/vuja", RepositoryRoot: "~/Developer/vuja", CodexAccount: "person@example.test",
		Git: gitStatusSnapshot{Branch: "main", Resolved: true}, Versions: map[string]string{"go": "1.26.0"},
		Environment: []string{"mise"}, CPU: 42, Memory: 58, HasCPU: true, HasMemory: true,
		ExitCode: &exit, Duration: 84 * time.Millisecond,
	}
	if scenario == "conflicts" {
		snapshot.Git.Conflicts = 2
		snapshot.Git.Operation = "merge"
	}
	c.SetStatusSnapshot(snapshot)
	prompt := func(text string) {
		c.SetMultilineInput(strings.Contains(text, "\n"))
		c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
		c.WritePTY([]byte(visual.UI.Chatbox.Prompt))
		c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
		c.WritePTY([]byte(text))
	}
	if scenario == "history" {
		prompt("go test ./example")
		c.WritePTY(terminalMarkerBytes(marker, "command-start"))
		c.WritePTY([]byte("compiled 3 packages\r\nall checks passed\r\n"))
		now = now.Add(84 * time.Millisecond)
		c.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
		prompt("")
	} else {
		prompt(input)
		if scenario == "running" {
			c.WritePTY(terminalMarkerBytes(marker, "command-start"))
			now = now.Add(12 * time.Second)
			c.RefreshRunningStatus()
		}
	}
	// Inspect the final physical model before Close emits teardown sequences.
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	if _, err := screen.Write(output.Bytes()); err != nil {
		return "", err
	}
	lines := make([]string, height)
	first := height
	for y := range height {
		line := uv.NewLine(width)
		for x := range width {
			if cell := screen.CellAt(x, y); cell != nil && cell.Width != 0 {
				line.Set(x, cell)
			}
		}
		lines[y] = terminalDefaultBackground + line.Render() + "\x1b[0m"
		if first == height && (strings.TrimSpace(ansi.Strip(lines[y])) != "" || y >= height-c.surfaceRows) {
			first = y
		}
	}
	return header + "\n" + strings.Join(lines[first:], "\n") + "\n", nil
}

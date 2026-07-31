package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/spec"
	"github.com/muesli/termenv"
)

func TestThemeFromConfigUsesResolvedBackgroundWithoutLateDetection(t *testing.T) {
	originalProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(originalProfile)
	})

	lipgloss.SetHasDarkBackground(false)
	nightTheme := ThemeFromConfig(config.DefaultConfig().UI.Colors, true)
	night := lipgloss.NewStyle().Foreground(nightTheme.Border).Render("border")
	if !strings.Contains(night, "\x1b[38;2;115;158;232m") {
		t.Fatalf("expected Serein Night border color, got %q", night)
	}

	lipgloss.SetHasDarkBackground(true)
	dayTheme := ThemeFromConfig(config.DefaultConfig().UI.Colors, false)
	day := lipgloss.NewStyle().Foreground(dayTheme.Border).Render("border")
	if !strings.Contains(day, "\x1b[38;2;29;103;246m") {
		t.Fatalf("expected Serein Day border color, got %q", day)
	}
}

func TestRenderGhostText_CursorAtEnd(t *testing.T) {
	o := NewOverlay(false)
	items := []spec.Suggestion{
		{Cmd: "git checkout -b feature"},
	}
	o.UpdateItems(items)

	// case 1: cursor at end of buffer -> should render ghost text suffix
	out := o.RenderGhostText("git check", false, true)
	if !strings.Contains(out, "out -b feature") {
		t.Fatalf("Expected ghost text suffix 'out -b feature', got: %q", out)
	}
	if o.LastGhostLen == 0 {
		t.Fatalf("Expected LastGhostLen > 0, got %d", o.LastGhostLen)
	}

	// case 2: cursor moved left (cursorAtEnd == false) -> should clear ghost text
	outClear := o.RenderGhostText("git check", false, false)
	if strings.Contains(outClear, "out -b feature") {
		t.Fatalf("Expected ghost text to be hidden/cleared when cursor moved left, got: %q", outClear)
	}
	if o.LastGhostLen != 0 {
		t.Fatalf("Expected LastGhostLen == 0 after clearing, got %d", o.LastGhostLen)
	}
}

func TestBottomPromptGhostTextUsesChatboxSurfaceBackground(t *testing.T) {
	originalProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(originalProfile)
	})

	o := NewOverlay(true)
	o.SetBottomPrompt(true)
	o.UpdateItems([]spec.Suggestion{{Cmd: "git checkout"}})

	rendered := o.RenderGhostText("git ", false, true)
	if !strings.Contains(rendered, "48;2;36;37;40") {
		t.Fatalf("expected ghost text to use the night chatbox surface background, got %q", rendered)
	}

	cleared := o.RenderGhostText("git ", false, false)
	if !strings.Contains(cleared, "48;2;36;37;40") {
		t.Fatalf("expected cleared ghost text cells to retain the chatbox surface background, got %q", cleared)
	}
}

func TestGetGhostText(t *testing.T) {
	o := NewOverlay(false)
	items := []spec.Suggestion{
		{Cmd: "docker exec -it my-container bash"},
	}
	o.UpdateItems(items)

	// case 1: cursor at end
	ghost := o.GetGhostText("docker e", true)
	expected := "xec -it my-container bash"
	if ghost != expected {
		t.Fatalf("Expected %q, got %q", expected, ghost)
	}

	// case 2: cursor not at end (moved left)
	ghostLeft := o.GetGhostText("docker e", false)
	if ghostLeft != "" {
		t.Fatalf("Expected empty string when cursor not at end, got %q", ghostLeft)
	}

	// case 3: user navigated menu with Up/Down arrow -> should sync with highlighted item
	o.SetUserNavigated(true)
	ghostNav := o.GetGhostText("docker e", true)
	if ghostNav != expected {
		t.Fatalf("Expected %q when user navigated menu, got %q", expected, ghostNav)
	}
}

func TestGetNextGhostTextAcceptsOneTokenOrPathSegment(t *testing.T) {
	tests := []struct {
		buffer     string
		suggestion string
		expected   string
	}{
		{buffer: "git", suggestion: "git checkout feature", expected: " checkout "},
		{buffer: "docker r", suggestion: "docker run --rm", expected: "un "},
		{buffer: "cd src/", suggestion: "cd src/components/button", expected: "components/"},
	}
	for _, test := range tests {
		o := NewOverlay(false)
		o.UpdateItems([]spec.Suggestion{{Cmd: test.suggestion}})
		if got := o.GetNextGhostText(test.buffer, true); got != test.expected {
			t.Fatalf("buffer %q: expected %q, got %q", test.buffer, test.expected, got)
		}
	}
}

func TestGhostText_MenuSync(t *testing.T) {
	o := NewOverlay(false)
	items := []spec.Suggestion{
		{Cmd: "git checkout -b first"},
		{Cmd: "git checkout master"},
	}
	o.UpdateItems(items)

	// default item 0
	ghost0 := o.GetGhostText("git check", true)
	if ghost0 != "out -b first" {
		t.Fatalf("Expected 'out -b first', got %q", ghost0)
	}

	// move cursor down to item 1
	o.MoveCursor("down")
	ghost1 := o.GetGhostText("git check", true)
	if ghost1 != "out master" {
		t.Fatalf("Expected 'out master', got %q", ghost1)
	}

	out := o.RenderGhostText("git check", true, true)
	if !strings.Contains(out, "out master") {
		t.Fatalf("Expected RenderGhostText to render 'out master', got %q", out)
	}
}

func TestTabNavigationFocusesFirstAndCyclesWithWraparound(t *testing.T) {
	o := NewOverlay(false)
	o.UpdateItems([]spec.Suggestion{
		{Cmd: "git status"},
		{Cmd: "git switch main"},
	})

	if count := o.SuggestionCount(); count != 2 {
		t.Fatalf("expected two suggestions, got %d", count)
	}
	if selected := o.FocusFirst(); selected != "git status" {
		t.Fatalf("expected first suggestion, got %q", selected)
	}
	if selected := o.CycleCursor(); selected != "git switch main" {
		t.Fatalf("expected second suggestion, got %q", selected)
	}
	if selected := o.CycleCursor(); selected != "git status" {
		t.Fatalf("expected cycling to wrap to first suggestion, got %q", selected)
	}
}

func TestHistorySearchRendersQueryAndScope(t *testing.T) {
	o := NewOverlay(false)
	o.SetHistorySearch("dock", "project success", []spec.Suggestion{{Cmd: "docker ps"}})

	rendered := o.Render()
	if !strings.Contains(rendered, "project success: dock") {
		t.Fatalf("expected history search context in overlay, got %q", rendered)
	}
}

func TestOverlayFooterUsesConfiguredKeybindings(t *testing.T) {
	cfg := config.Get()
	original := cfg.Keybindings
	cfg.Keybindings.Accept = "ctrl+n"
	cfg.Keybindings.HistorySearch = "ctrl+g"
	t.Cleanup(func() {
		cfg.Keybindings = original
	})

	o := NewOverlay(false)
	o.UpdateItems([]spec.Suggestion{{Cmd: "git status"}})
	rendered := o.Render()

	for _, expected := range []string{"<Ctrl+N>", "<Ctrl+G>"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected footer to contain %q, got %q", expected, rendered)
		}
	}
	if !strings.Contains(rendered, "Next/Accept") {
		t.Fatalf("expected footer to describe Tab navigation, got %q", rendered)
	}
}

func TestHistoryColumnLayoutKeepsCommandAsTheResponsiveColumn(t *testing.T) {
	wide := historyColumnLayout(72, config.HistoryUIConfig{ShowExitStatus: true, ShowCwd: true})
	if !wide.ShowDuration || !wide.ShowRelativeTime || !wide.ShowExitStatus || !wide.ShowCwd {
		t.Fatalf("expected all configured metadata columns on a wide row, got %+v", wide)
	}
	if wide.CommandWidth <= wide.CwdWidth {
		t.Fatalf("expected command to remain the expanding column, got %+v", wide)
	}

	narrow := historyColumnLayout(34, config.HistoryUIConfig{ShowExitStatus: true, ShowCwd: true})
	if narrow.ShowCwd {
		t.Fatalf("expected cwd to be dropped first on a narrow row, got %+v", narrow)
	}
	if narrow.CommandWidth < 12 {
		t.Fatalf("expected usable command space, got %+v", narrow)
	}
}

func TestHistoryCommandSegmentsPreserveEveryMatch(t *testing.T) {
	segments := historyCommandSegments("déploy deploy/api", []MatchRange{
		{Start: 0, End: 1},
		{Start: 2, End: 4},
		{Start: 7, End: 13},
	}, 40)

	var matched []string
	for _, segment := range segments {
		if segment.Matched {
			matched = append(matched, segment.Text)
		}
	}
	if got := strings.Join(matched, "|"); got != "d|pl|deploy" {
		t.Fatalf("expected repeated and non-contiguous match segments, got %q from %+v", got, segments)
	}
}

func TestRichHistorySearchRendersExecutionMetadata(t *testing.T) {
	o := NewOverlay(false)
	o.SetRichHistorySearch("deploy", "global", []RichHistoryResult{{
		HistoryEntry: HistoryEntry{
			Command:     "just deploy staging",
			Cwd:         "/repo",
			Duration:    45 * time.Second,
			ExitCode:    0,
			HasExitCode: true,
		},
		RelativeTime: "1h ago",
		MatchRanges:  []MatchRange{{Start: 5, End: 11}},
	}})

	rendered := o.Render()
	for _, expected := range []string{"45s", "1h ago", "just ", "deploy", " staging"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rich history row to contain %q, got %q", expected, rendered)
		}
	}
}

func TestRichHistorySearchStaysVisibleWhenScopeHasNoMatches(t *testing.T) {
	o := NewOverlay(false)
	o.SetRichHistorySearch("deploy", "directory", nil)

	rendered := o.Render()
	if !o.IsVisible() {
		t.Fatal("expected rich history search to remain visible")
	}
	if !strings.Contains(rendered, "No history matches in this scope") {
		t.Fatalf("expected an empty-state row, got %q", rendered)
	}
	if selected := o.GetCurrentCmd(); selected != "" {
		t.Fatalf("expected empty state to be non-selectable, got %q", selected)
	}
}

func TestVisibleItemLimitHonorsConfigurationAndTerminalHeight(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		rows       int
		expected   int
	}{
		{name: "configured limit", configured: 15, rows: 40, expected: 15},
		{name: "terminal limit", configured: 15, rows: 10, expected: 7},
		{name: "small terminal", configured: 6, rows: 4, expected: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := visibleItemLimit(test.configured, test.rows); got != test.expected {
				t.Fatalf("expected %d visible items, got %d", test.expected, got)
			}
		})
	}
}

func TestBottomPromptSuggestionMenuOpensAndClosesUpward(t *testing.T) {
	useTerminalSize(t, 120, 24)
	cfg := config.Get()
	original := cfg.UI.PromptPosition
	cfg.UI.PromptPosition = "bottom"
	t.Cleanup(func() { cfg.UI.PromptPosition = original })

	screen := vt.NewEmulator(120, 24)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write([]byte("\x1b[24;1Hλ git"))

	o := NewOverlay(false)
	o.SetPromptRows(1)
	o.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git status", Desc: "status"}})
	_, _ = screen.Write([]byte(o.Render()))

	if got := terminalRowText(screen, 23); !strings.Contains(got, "λ git") {
		t.Fatalf("expected prompt to remain on the last row, got %q", got)
	}
	if !terminalRowsContain(screen, 0, 23, "git status") {
		t.Fatal("expected suggestion menu above the prompt")
	}

	_, _ = screen.Write([]byte(o.Clear()))
	if got := terminalRowText(screen, 23); !strings.Contains(got, "λ git") {
		t.Fatalf("expected closing menu to preserve prompt, got %q", got)
	}
	if terminalRowsContain(screen, 0, 23, "git status") {
		t.Fatal("expected closing menu to clear rows above the prompt")
	}
}

func TestBottomPromptSuggestionMenuStaysLeftAlignedWhileQueryGrows(t *testing.T) {
	useTerminalSize(t, 120, 24)

	for _, query := range []string{"g", "cd hello world"} {
		t.Run(query, func(t *testing.T) {
			screen := vt.NewEmulator(120, 24)
			t.Cleanup(func() { _ = screen.Close() })
			_, _ = screen.Write([]byte("\x1b[24;1H› " + query))

			o := NewOverlay(false)
			o.SetBottomPrompt(true)
			o.SetPromptRows(1)
			o.SetPromptLen(2)
			o.SetQueryAndItems(query, []spec.Suggestion{{Cmd: query + " suggestion"}})
			rendered := o.Render()
			_, _ = screen.Write([]byte(rendered))

			if strings.ContainsAny(rendered, "╭╮╰╯│─") {
				t.Fatalf("expected a borderless bottom menu for %q, got %q", query, rendered)
			}
			marker := screen.CellAt(2, o.LastMenuTopRow+1)
			if marker == nil || marker.Content != strings.TrimSpace(config.Get().UI.Chatbox.Prompt) {
				t.Fatalf("expected menu marker aligned with the prompt for %q, got %+v", query, marker)
			}
		})
	}
}

func TestBottomPromptSuggestionMenuUsesFullWidthAlignedPromptMarkerAndRightBadges(t *testing.T) {
	useTerminalSize(t, 120, 24)
	screen := vt.NewEmulator(120, 24)
	t.Cleanup(func() { _ = screen.Close() })

	o := NewOverlay(true)
	o.SetBottomPrompt(true)
	o.SetPromptRows(4)
	o.SetQueryAndItems("cd", []spec.Suggestion{
		{Cmd: "cd previous/", Desc: "history", Source: "history"},
		{Cmd: "cd cmd/", Desc: "directory", Source: "filesystem"},
		{Cmd: "cd ~/Developer/vuja", Desc: "visited directory", Source: "directory-index"},
		{Cmd: "cd feature", Desc: "learned argument", Source: "argument"},
	})
	rendered := o.Render()
	_, _ = screen.Write([]byte(rendered))

	top := o.LastMenuTopRow
	if strings.ContainsAny(rendered, "╭╮╰╯│─") {
		t.Fatalf("expected the bottom overlay to render without an outline, got %q", rendered)
	}
	if marker := screen.CellAt(2, top+1); marker == nil || marker.Content != strings.TrimSpace(config.Get().UI.Chatbox.Prompt) {
		t.Fatalf("expected overlay marker aligned with the padded prompt, got %+v", marker)
	}
	for row, badge := range []string{"history", "directory", "visited", "learned"} {
		line := terminalRowText(screen, top+row+1)
		if !strings.HasSuffix(line, " "+badge) {
			t.Fatalf("expected %q badge against the right edge, got %q", badge, line)
		}
	}
	for _, color := range []string{"48;2;97;255;207", "48;2;97;238;255", "48;2;115;158;232", "48;2;253;125;244"} {
		if !strings.Contains(rendered, color) {
			t.Fatalf("expected distinct badge color %q, got %q", color, rendered)
		}
	}
}

func TestClassicPromptSuggestionMenuPreservesLegacyDescriptionsAndMarker(t *testing.T) {
	useTerminalSize(t, 120, 24)
	o := NewOverlay(true)
	o.SetQueryAndItems("cd", []spec.Suggestion{{
		Cmd: "cd ~/Developer/vuja", Desc: "visited directory", Source: "directory-index",
	}})

	rendered := o.Render()
	if !strings.Contains(rendered, "visited directory") || !strings.Contains(rendered, "▶") ||
		!strings.ContainsAny(rendered, "╭╮╰╯│─") {
		t.Fatalf("expected classic overlay border, description, and selection marker, got %q", rendered)
	}
}

func TestBottomPromptHistoryMenuUsesRowsAboveMultilinePrompt(t *testing.T) {
	useTerminalSize(t, 120, 24)
	cfg := config.Get()
	original := cfg.UI.PromptPosition
	cfg.UI.PromptPosition = "bottom"
	t.Cleanup(func() { cfg.UI.PromptPosition = original })

	screen := vt.NewEmulator(120, 24)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write([]byte("\x1b[23;1Hcontext\r\n\x1b[24;1Hλ search"))

	o := NewOverlay(false)
	o.SetPromptRows(2)
	o.SetHistorySearch("deploy", "global", []spec.Suggestion{{Cmd: "just deploy"}})
	_, _ = screen.Write([]byte(o.Render()))

	if got := terminalRowText(screen, 22); !strings.Contains(got, "context") {
		t.Fatalf("expected first prompt row to remain fixed, got %q", got)
	}
	if got := terminalRowText(screen, 23); !strings.Contains(got, "λ search") {
		t.Fatalf("expected editable prompt row to remain fixed, got %q", got)
	}
	if !terminalRowsContain(screen, 0, 22, "just deploy") {
		t.Fatal("expected history menu above the complete prompt surface")
	}
}

func TestBottomPromptGhostTextStaysOnInputRow(t *testing.T) {
	useTerminalSize(t, 120, 24)
	cfg := config.Get()
	original := cfg.UI.PromptPosition
	cfg.UI.PromptPosition = "bottom"
	t.Cleanup(func() { cfg.UI.PromptPosition = original })

	screen := vt.NewEmulator(120, 24)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write([]byte("\x1b[24;1Hλ git"))

	o := NewOverlay(false)
	o.SetPromptLen(2)
	o.UpdateItems([]spec.Suggestion{{Cmd: "git status"}})
	_, _ = screen.Write([]byte(o.RenderGhostText("git", false, true)))

	if got := terminalRowText(screen, 23); !strings.Contains(got, "λ git status") {
		t.Fatalf("expected ghost text aligned on bottom input row, got %q", got)
	}
	if got := screen.CursorPosition().Y; got != 23 {
		t.Fatalf("expected ghost text rendering to restore bottom cursor, got %d", got)
	}
}

func TestBottomPromptTransientRegionIncludesGhostTextRow(t *testing.T) {
	o := NewOverlay(false)
	o.SetBottomPrompt(true)
	o.SetPromptRows(3)
	o.LastTerminalHeight = 24
	o.LastMenuTopRow = 12
	o.LastWindowSize = 5
	o.LastGhostLen = 8

	top, rows := o.BottomRegion()
	if top != 12 || rows != 11 {
		t.Fatalf("expected restoration to cover menu rows through editable row 22, got top=%d rows=%d", top, rows)
	}
}

func TestBottomPromptSuggestionMenuReflowsAfterResize(t *testing.T) {
	width, height := 120, 24
	originalSize := terminalSize
	terminalSize = func(int) (int, int, error) { return width, height, nil }
	t.Cleanup(func() { terminalSize = originalSize })

	cfg := config.Get()
	originalPosition := cfg.UI.PromptPosition
	cfg.UI.PromptPosition = "bottom"
	t.Cleanup(func() { cfg.UI.PromptPosition = originalPosition })

	screen := vt.NewEmulator(width, height)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write([]byte("\x1b[24;1Hλ git"))

	o := NewOverlay(false)
	o.SetPromptRows(1)
	o.SetQueryAndItems("git", []spec.Suggestion{
		{Cmd: "git status"},
		{Cmd: "git switch"},
		{Cmd: "git show"},
		{Cmd: "git stash"},
	})
	_, _ = screen.Write([]byte(o.Render()))

	height = 8
	screen.Resize(width, height)
	_, _ = screen.Write([]byte("\x1b[8;1H\x1b[2Kλ git"))
	o.InvalidateGeometry()
	_, _ = screen.Write([]byte(o.Render()))

	if got := terminalRowText(screen, 7); !strings.Contains(got, "λ git") {
		t.Fatalf("expected prompt fixed after resize, got %q", got)
	}
	if !terminalRowsContain(screen, 0, 7, "git status") {
		t.Fatal("expected resized suggestion menu above the prompt")
	}
}

func TestGhostText_Truncation(t *testing.T) {
	o := NewOverlay(false)
	longCmd := "git commit -m '" + strings.Repeat("a", 150) + "'"
	items := []spec.Suggestion{
		{Cmd: longCmd},
	}
	o.UpdateItems(items)
	o.SetPromptLen(10)

	// typed query length 105 -> total cursor col = 115, default width = 120 -> available cols = 5
	typedQuery := "git commit -m '" + strings.Repeat("a", 90)
	out := o.RenderGhostText(typedQuery, false, true)
	if !strings.Contains(out, "…") {
		t.Fatalf("Expected truncated ghost text with '…', got %q", out)
	}
}

func TestHideMenu_PreservesTypedQueryForAI(t *testing.T) {
	o := NewOverlay(false)
	o.HideMenu("git commit")

	if o.GetTypedQuery() != "git commit" {
		t.Fatalf("Expected TypedQuery to be preserved as 'git commit', got %q", o.GetTypedQuery())
	}

	aiSugg := spec.Suggestion{
		Cmd:        "git commit -m 'fix: test'",
		Desc:       "AI suggestion",
		Source:     "ai",
		Confidence: 85,
	}
	if !o.InjectAISuggestion(aiSugg) {
		t.Fatalf("Expected InjectAISuggestion to succeed after HideMenu")
	}
	if !o.IsVisible() || len(o.Items) == 0 || o.Items[0].Cmd != aiSugg.Cmd {
		t.Fatalf("Expected AI suggestion to be injected into Items[0] and Visible=true")
	}
}

func terminalRowText(screen *vt.Emulator, row int) string {
	var line strings.Builder
	for column := 0; column < screen.Width(); column++ {
		cell := screen.CellAt(column, row)
		if cell != nil && cell.Width > 0 {
			line.WriteString(cell.Content)
		}
	}
	return strings.TrimRight(line.String(), " ")
}

func terminalRowsContain(screen *vt.Emulator, start, end int, expected string) bool {
	for row := start; row < end; row++ {
		if strings.Contains(terminalRowText(screen, row), expected) {
			return true
		}
	}
	return false
}

func useTerminalSize(t *testing.T, width, height int) {
	t.Helper()
	original := terminalSize
	terminalSize = func(int) (int, int, error) { return width, height, nil }
	t.Cleanup(func() { terminalSize = original })
}

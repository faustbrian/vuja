package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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

func TestHistorySearchRendersQueryAndScope(t *testing.T) {
	o := NewOverlay(false)
	o.SetHistorySearch("dock", "project success", []spec.Suggestion{{Cmd: "docker ps"}})

	rendered := o.Render()
	if !strings.Contains(rendered, "project success: dock") {
		t.Fatalf("expected history search context in overlay, got %q", rendered)
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

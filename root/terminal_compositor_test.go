package root

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/text/unicode/norm"
)

type gatedTerminalWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *gatedTerminalWriter) Write(data []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return len(data), nil
}

func TestAsyncTerminalWriterKeepsRenderingOffTheStateLock(t *testing.T) {
	sink := &gatedTerminalWriter{started: make(chan struct{}), release: make(chan struct{})}
	writer := newAsyncTerminalWriter(sink)
	if written, err := writer.Write([]byte("frame")); err != nil || written != len("frame") {
		t.Fatalf("unexpected enqueue result written=%d err=%v", written, err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("terminal worker did not receive the frame")
	}
	close(sink.release)
	writer.Close()
}

func TestBottomCompositorAnchorsPromptAndInputToLastRow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("\x1b[32mλ\x1b[0m "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("git status"))

	screen := vt.NewEmulator(20, 6)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write(output.Bytes())

	if got := screen.CursorPosition().Y; got != 5 {
		t.Fatalf("expected input cursor on last row, got row %d", got)
	}
	if got := screenLine(screen, 5); !strings.Contains(got, "λ git status") {
		t.Fatalf("expected prompt and input on last row, got %q", got)
	}
}

func TestBottomCompositorOwnsBracketedPasteModeAtThePrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("$ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	if !strings.Contains(output.String(), terminalBracketedPasteEnable) {
		t.Fatalf("expected bottom compositor to enable bracketed paste, got %q", output.String())
	}

	output.Reset()
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	if !strings.Contains(output.String(), terminalBracketedPasteDisable) {
		t.Fatalf("expected foreground commands to regain bracketed paste control, got %q", output.String())
	}

	output.Reset()
	compositor.Close()
	if !strings.Contains(output.String(), terminalBracketedPasteDisable) {
		t.Fatalf("expected compositor close to restore bracketed paste mode, got %q", output.String())
	}
}

func TestBottomCompositorRendersThemedPaddingAroundShellOwnedInput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(terminalInputBoxTheme{
		Background:        "#080a0d",
		SurfaceBackground: "#242528",
		StatusBackground:  "#080a0d",
		StatusText:        "#c6cad7",
		Border:            "#112233",
		Accent:            "#445566",
		Muted:             "#747579",
	})
	compositor.SetChatboxConfig(terminalChatboxConfig{Prompt: "λ "})
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	compositor.SetInputBoxPath(filepath.Join(home, "Developer", "vuja"))
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("git status"))

	screen := applyTerminalOutput(t, output.Bytes(), 30, 8)
	if got := screenLine(screen, 5); strings.TrimSpace(got) != "" {
		t.Fatalf("expected untitled top padding, got %q", got)
	}
	if got := screenLine(screen, 6); !strings.HasPrefix(got, "  λ git status") || strings.ContainsAny(got, "│╭╮╰╯") {
		t.Fatalf("expected shell-owned prompt in a padded borderless surface, got %q", got)
	}
	if got := screenLine(screen, 7); strings.TrimSpace(got) != "" {
		t.Fatalf("expected bottom input padding, got %q", got)
	}
	if got := screen.CursorPosition(); got.Y != 6 || got.X != 14 {
		t.Fatalf("expected shell cursor inside chatbox content, got %+v", got)
	}
	for _, color := range []string{"48;2;36;37;40", "38;2;68;85;102"} {
		if !strings.Contains(output.String(), color) {
			t.Fatalf("expected configured chatbox color %q in rendered output", color)
		}
	}
}

func TestBottomCompositorNormalizesReverseVideoInsideTheInputSurface(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{Prompt: "› "})
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\x1b[7mpasted text\x1b[27m"))

	screen := applyTerminalOutput(t, output.Bytes(), 30, 8)
	cell := screen.CellAt(4, 6)
	if cell == nil || cell.Content != "p" {
		t.Fatalf("expected pasted text inside chatbox, got %#v", cell)
	}
	if cell.Style.Attrs&uv.AttrReverse != 0 {
		t.Fatal("expected chatbox rendering to remove reverse video from pasted text")
	}
	if cell.Style.Bg == nil {
		t.Fatal("expected pasted text to retain chatbox background")
	}
	red, green, blue, _ := cell.Style.Bg.RGBA()
	if red != 0x2424 || green != 0x2525 || blue != 0x2828 {
		t.Fatalf("expected pasted text to retain #242528 background, got #%04x%04x%04x", red, green, blue)
	}
}

func TestBottomCompositorRendersPaddedBorderlessInputAndStatusSurfaces(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 40, 10)
	compositor.SetInputBoxTheme(terminalInputBoxTheme{
		Background:        "#080a0d",
		SurfaceBackground: "#242528",
		StatusBackground:  "#080a0d",
		StatusText:        "#c6cad7",
		Border:            "#739ee8",
		Accent:            "#61ffcf",
		Muted:             "#404658",
	})
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Prompt:    "› ",
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left: []string{"directory"}, Right: []string{"exit"},
		},
	})
	compositor.SetInputBoxPath("/tmp/project")
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("git status"))

	screen := applyTerminalOutput(t, output.Bytes(), 40, 10)
	if got := screenLine(screen, 6); strings.TrimSpace(got) != "" {
		t.Fatalf("expected top input padding without a title, got %q", got)
	}
	if got := screenLine(screen, 7); !strings.HasPrefix(got, "  › git status") || strings.ContainsAny(got, "│╭╮") {
		t.Fatalf("expected padded borderless input surface, got %q", got)
	}
	if got := screenLine(screen, 8); strings.TrimSpace(got) != "" {
		t.Fatalf("expected bottom input padding, got %q", got)
	}
	if got := screenLine(screen, 9); !strings.HasPrefix(got, "  /tmp/project") || strings.ContainsAny(got, "│├┤╰╯") {
		t.Fatalf("expected borderless status row, got %q", got)
	}
	if got := screen.CursorPosition(); got.X != 14 || got.Y != 7 {
		t.Fatalf("expected cursor inside the input surface, got %+v", got)
	}
	for _, color := range []string{"48;2;36;37;40", "48;2;8;10;13", "38;2;97;255;207"} {
		if !strings.Contains(output.String(), color) {
			t.Fatalf("expected distinct chatbox background %q", color)
		}
	}

	output.Reset()
	compositor.Resize(40, 5)
	compact := applyTerminalOutput(t, output.Bytes(), 40, 5)
	if terminalContainsLine(compact, "/tmp/project") {
		t.Fatalf("expected status surface to collapse before input space is lost, got %q", terminalScreenLines(compact))
	}
	if got := compact.CursorPosition(); got.Y != 3 {
		t.Fatalf("expected compact input cursor to remain usable, got %+v", got)
	}
}

func TestBottomCompositorPlacesTitleAndStatusRegionsIndependently(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "bottom", "test-session", 80, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title: terminalChatboxBarConfig{
			Left: []string{"directory"}, Center: []string{"git-branch"}, Right: []string{"versions"},
		},
		Status: terminalChatboxBarConfig{
			Left: []string{"git-status"}, Center: []string{"duration"}, Right: []string{"exit"},
		},
	})
	exitCode := 0
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/tmp/project",
		Git:       gitStatusSnapshot{Branch: "main", Modified: 2},
		Versions:  map[string]string{"go": "1.26.0"},
		Duration:  42 * time.Millisecond,
		ExitCode:  &exitCode,
	})
	t.Cleanup(compositor.Close)

	title := ansi.Strip(compositor.inputBoxTitleLine())
	status := ansi.Strip(compositor.inputBoxStatusLine())
	for _, item := range []struct {
		line string
		text string
		want terminalStatusAlignment
	}{
		{title, "/tmp/project", terminalStatusLeft},
		{title, "main", terminalStatusCenter},
		{title, "Go 1.26.0", terminalStatusRight},
		{status, "modified 2", terminalStatusLeft},
		{status, "42ms", terminalStatusCenter},
		{status, "exit 0", terminalStatusRight},
	} {
		column := strings.Index(item.line, item.text)
		switch item.want {
		case terminalStatusLeft:
			if column != terminalInputHorizontalPadding {
				t.Fatalf("expected %q left aligned, got %q", item.text, item.line)
			}
		case terminalStatusCenter:
			center := (80 - len(item.text)) / 2
			if column < center-1 || column > center+1 {
				t.Fatalf("expected %q centered, got %q", item.text, item.line)
			}
		case terminalStatusRight:
			if column+len(item.text) != 80-terminalInputHorizontalPadding {
				t.Fatalf("expected %q right aligned, got %q", item.text, item.line)
			}
		}
	}
}

func TestChatboxStatusSeparatesGitFactsAndRightAlignsRuntimeContext(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 120, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left:  []string{"directory", "git-branch", "git-status"},
			Right: []string{"duration", "exit", "cpu", "memory"},
		},
		Colors: map[string]string{
			"git-modified": "#f3c969", "git-untracked": "#caa472", "git-ahead": "#00add8",
			"duration-fast": "#61ffcf", "exit-success": "#61ffcf", "load-average": "#f3c969",
		},
	})
	exitCode := 0
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/tmp/project",
		Git:       gitStatusSnapshot{Branch: "main", Modified: 3, Untracked: 2, Ahead: 1},
		Duration:  22 * time.Millisecond,
		ExitCode:  &exitCode,
		CPU:       52, Memory: 51, HasCPU: true, HasMemory: true,
	})

	line := ansi.Strip(compositor.inputBoxStatusLine())
	if !strings.Contains(line, "modified 3 · untracked 2 · ahead 1") {
		t.Fatalf("expected individually separated Git facts, got %q", line)
	}
	if !strings.HasSuffix(line, "22ms · exit 0 · CPU 52% · RAM 51%  ") {
		t.Fatalf("expected runtime context aligned against the right padding, got %q", line)
	}
}

func TestStatusThresholdColorsDistinguishDurationLoadAndExitStates(t *testing.T) {
	for _, test := range []struct {
		name string
		got  string
		want string
	}{
		{name: "fast duration", got: durationStatusColor(100 * time.Millisecond), want: "duration-fast"},
		{name: "average duration", got: durationStatusColor(1500 * time.Millisecond), want: "duration-average"},
		{name: "slow duration", got: durationStatusColor(8 * time.Second), want: "duration-slow"},
		{name: "low load", got: loadStatusColor("cpu", 20), want: "cpu-low"},
		{name: "average load", got: loadStatusColor("cpu", 60), want: "cpu-average"},
		{name: "high load", got: loadStatusColor("memory", 82), want: "memory-high"},
		{name: "critical load", got: loadStatusColor("memory", 96), want: "memory-critical"},
		{name: "successful exit", got: exitStatusColor(0), want: "exit-success"},
		{name: "failed exit", got: exitStatusColor(1), want: "exit-failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("expected %q, got %q", test.want, test.got)
			}
		})
	}
}

func TestChatboxStatusUsesNeutralExitBeforeTheFirstCommand(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status:    terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
		Colors:    map[string]string{"exit-neutral": "#747579"},
	})

	line := compositor.inputBoxStatusLine()
	if plain := ansi.Strip(line); !strings.Contains(plain, "exit —") || strings.Contains(plain, "0ms") {
		t.Fatalf("expected a neutral exit without an initial duration, got %q", plain)
	}
	if !strings.Contains(line, "38;2;116;117;121m") {
		t.Fatalf("expected the neutral exit color, got %q", line)
	}
}

func TestChatboxStatusIgnoresStartupAndEmptySubmissionCompletionMarkers(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status:    terminalChatboxBarConfig{Right: []string{"exit"}},
	})

	compositor.WritePTY(terminalMarkerBytes("test-session", "command-end:0"))
	if plain := ansi.Strip(compositor.inputBoxStatusLine()); !strings.Contains(plain, "exit —") {
		t.Fatalf("expected startup completion to preserve neutral state, got %q", plain)
	}

	compositor.WritePTY(terminalMarkerBytes("test-session", "command-start"))
	compositor.WritePTY(terminalMarkerBytes("test-session", "command-end:7"))
	if plain := ansi.Strip(compositor.inputBoxStatusLine()); !strings.Contains(plain, "exit 7") {
		t.Fatalf("expected completed command state, got %q", plain)
	}

	compositor.WritePTY(terminalMarkerBytes("test-session", "command-end:0"))
	if plain := ansi.Strip(compositor.inputBoxStatusLine()); !strings.Contains(plain, "exit 7") {
		t.Fatalf("expected empty submission to retain the last command state, got %q", plain)
	}
}

func TestChatboxVersionsRenderFromApplicationLayerDownToInfrastructure(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 180, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status:    terminalChatboxBarConfig{Left: []string{"versions"}},
	})
	compositor.SetStatusSnapshot(statusSnapshot{Versions: map[string]string{
		"laravel": "13.15.0", "php": "8.3", "composer": "2.8",
		"python": "3.13", "ruby": "3.4", "elixir": "1.18",
		"node": "24", "bun": "1.2", "go": "1.26", "rust": "1.88",
		"docker-compose": "5.3.1", "docker": "29.6.2",
	}})

	got := strings.TrimSpace(ansi.Strip(compositor.inputBoxStatusLine()))
	want := "Laravel 13.15.0 · PHP 8.3 · Composer 2.8 · Python 3.13 · Ruby 3.4 · Elixir 1.18 · Node 24 · Bun 1.2 · Go 1.26 · Rust 1.88 · Compose 5.3.1 · Docker 29.6.2"
	if got != want {
		t.Fatalf("expected versions ordered by dependency layer, got %q", got)
	}
}

func TestRepositoryAwarePathSupportsHierarchyAndSegmentLimit(t *testing.T) {
	t.Setenv("HOME", "/Users/brian")
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 120, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		PathColorMode:   "hierarchy",
		PathMaxSegments: 5,
		Colors: map[string]string{
			"directory":      "#61ffcf",
			"directory-root": "#739ee8",
		},
	})
	snapshot := statusSnapshot{
		Directory:      "/Users/brian/Developer/go-libraries/pkg/service/path/another/path",
		RepositoryRoot: "/Users/brian/Developer/go-libraries",
	}

	rendered := compositor.repositoryAwarePath(snapshot)
	if got := ansi.Strip(rendered); got != "~/Developer/go-libraries/…/path" {
		t.Fatalf("expected repository anchor and final segment, got %q", got)
	}
	if strings.Count(rendered, "\x1b[38;2;") < 4 {
		t.Fatalf("expected hierarchical segment colors, got %q", rendered)
	}

	compositor.SetChatboxConfig(terminalChatboxConfig{
		PathColorMode:   "single",
		PathMaxSegments: 5,
		Colors: map[string]string{
			"directory":      "#61ffcf",
			"directory-root": "#739ee8",
		},
	})
	rendered = compositor.repositoryAwarePath(snapshot)
	if strings.Count(rendered, "\x1b[38;2;") != 1 {
		t.Fatalf("expected one path color in single mode, got %q", rendered)
	}

	deepRoot := statusSnapshot{
		Directory:      "/Users/brian/Developer/clients/acme/platform/service/path",
		RepositoryRoot: "/Users/brian/Developer/clients/acme/platform",
	}
	if got := ansi.Strip(compositor.repositoryAwarePath(deepRoot)); got != "~/…/platform/…/path" {
		t.Fatalf("expected a deep repository anchor to retain its basename, got %q", got)
	}
	deepRoot.Directory = deepRoot.RepositoryRoot
	if got := ansi.Strip(compositor.repositoryAwarePath(deepRoot)); got != "~/…/clients/acme/platform" {
		t.Fatalf("expected a deep repository root to render once, got %q", got)
	}
}

func TestChatboxStatusRendersFiniteColoredSegmentsAndCollapsesLowPriorityData(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 120, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left:  []string{"directory", "git-branch", "git-status", "git-added", "git-deleted", "versions"},
			Right: []string{"duration", "exit", "cpu", "memory"},
		},
		Colors: map[string]string{
			"directory": "#61ffcf", "git-branch": "#fd7df4", "php": "#777bb4",
			"exit-failure": "#ff7b72", "cpu": "#c6cad7", "memory": "#c6cad7",
		},
	})
	exitCode := 7
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/tmp/project", Git: gitStatusSnapshot{Branch: "main", Changed: 3, Modified: 3, Added: 2, Deleted: 1},
		Versions: map[string]string{"php": "8.4.1"}, Duration: 1250 * time.Millisecond,
		ExitCode: &exitCode, CPU: 12, Memory: 64,
	})

	wide := ansi.Strip(compositor.inputBoxStatusLine())
	ordered := []string{"/tmp/project", "main", "modified 3", "added 2", "deleted 1", "PHP 8.4.1", "1.25s", "exit 7", "CPU 12%", "RAM 64%"}
	last := -1
	for _, expected := range ordered {
		if !strings.Contains(wide, expected) {
			t.Fatalf("expected %q in wide status %q", expected, wide)
		}
		if index := strings.Index(wide, expected); index <= last {
			t.Fatalf("expected configured status order %v, got %q", ordered, wide)
		} else {
			last = index
		}
	}
	for _, char := range wide {
		if unicode.Is(unicode.Categories["Co"], char) {
			t.Fatalf("status bar must not render icons, got private-use rune %U", char)
		}
	}
	if !strings.Contains(compositor.inputBoxStatusLine(), "38;2;119;123;180m") {
		t.Fatal("expected PHP brand color in status output")
	}

	compositor.Resize(40, 8)
	narrow := ansi.Strip(strings.Join(compositor.inputBoxStatusLines(), " "))
	if strings.Contains(narrow, "CPU ") || strings.Contains(narrow, "RAM ") || strings.Contains(narrow, "PHP ") {
		t.Fatalf("expected low-priority segments to collapse, got %q", narrow)
	}
	if !strings.Contains(narrow, "main") || !strings.Contains(narrow, "exit 7") {
		t.Fatalf("expected branch and failure to survive collapse, got %q", narrow)
	}
}

func TestChatboxStatusUsesABoundedSecondRowBeforeDroppingContext(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 62, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left: []string{"directory", "git-branch", "git-status", "versions"}, Right: []string{"duration", "exit"},
		},
	})
	exitCode := 1
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/Users/developer/project", Git: gitStatusSnapshot{Branch: "feature/chatbox", Modified: 3},
		Duration: 1400 * time.Millisecond, ExitCode: &exitCode,
		Versions: map[string]string{"go": "1.26", "docker": "29.1"},
	})

	lines := compositor.inputBoxStatusLines()
	if len(lines) != 2 {
		t.Fatalf("expected two bounded status rows, got %d: %q", len(lines), lines)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) != 62 {
			t.Fatalf("expected full-width contained status row, got width %d", ansi.StringWidth(line))
		}
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	ordered := []string{"/Users/developer/project", "feature/chatbox", "modified 3", "Go 1.26", "Docker 29.1", "1.4s", "exit 1"}
	previous := -1
	for _, expected := range ordered {
		if !strings.Contains(plain, expected) {
			t.Fatalf("expected second-row layout to retain %q, got %q", expected, plain)
		}
		if index := strings.Index(plain, expected); index <= previous {
			t.Fatalf("expected second-row layout to preserve order %v, got %q", ordered, plain)
		} else {
			previous = index
		}
	}
}

func TestChatboxStatusUsesOneRowWhenHeightCannotSafelyFitTwo(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 62, 6)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left: []string{"directory", "git-branch", "git-status", "versions"}, Right: []string{"duration", "exit"},
		},
	})
	exitCode := 1
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/Users/developer/project", Git: gitStatusSnapshot{Branch: "feature/chatbox", Modified: 3},
		Duration: 1400 * time.Millisecond, ExitCode: &exitCode,
		Versions: map[string]string{"go": "1.26", "docker": "29.1"},
	})

	lines := compositor.inputBoxStatusLines()
	if len(lines) != 1 {
		t.Fatalf("expected one row when a second would consume editable space, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(ansi.Strip(lines[0]), "exit 1") {
		t.Fatalf("expected the failure to survive single-row collapse, got %q", ansi.Strip(lines[0]))
	}
}

func TestChatboxStatusKeepsFailureAheadOfLowerPriorityLeftSegments(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 28, 8)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left: []string{"directory", "git-branch", "git-status", "versions"}, Right: []string{"exit"},
		},
	})
	exitCode := 127
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/Users/developer/project",
		Git:       gitStatusSnapshot{Branch: "feature/long-branch", Resolved: true, Modified: 12, Untracked: 8},
		Versions:  map[string]string{"go": "1.26.0", "docker": "29.1.0"},
		ExitCode:  &exitCode,
	})

	status := ansi.Strip(strings.Join(compositor.inputBoxStatusLines(), " "))
	if !strings.Contains(status, "exit 127") {
		t.Fatalf("expected failure status to survive constrained layout, got %q", status)
	}
	if strings.Contains(status, "Go ") || strings.Contains(status, "Docker ") {
		t.Fatalf("expected lower-priority versions to collapse before failure, got %q", status)
	}
}

func TestChatboxStatusDoesNotRepaintAnUnchangedSnapshot(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "bottom", "test-session", 62, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{Separator: " · ", Status: terminalChatboxBarConfig{Left: []string{"directory", "git-branch"}}})
	compositor.WritePTY(terminalMarkerBytes("test-session", "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes("test-session", "prompt-end"))
	snapshot := statusSnapshot{Directory: "/tmp/project", Git: gitStatusSnapshot{Branch: "main"}}
	compositor.SetStatusSnapshot(snapshot)
	output.Reset()

	compositor.SetStatusSnapshot(snapshot)
	if output.Len() != 0 {
		t.Fatalf("expected unchanged status to avoid repaint, wrote %d bytes", output.Len())
	}
}

func TestChatboxStatusRepaintsOnlyChangedRows(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "bottom", "test-session", 62, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title:     terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:    terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	compositor.WritePTY(terminalMarkerBytes("test-session", "prompt-start"))
	compositor.WritePTY([]byte("› echo retained"))
	compositor.WritePTY(terminalMarkerBytes("test-session", "prompt-end"))
	output.Reset()

	exitCode := 0
	compositor.SetStatusSnapshot(statusSnapshot{
		Revision:  1,
		Directory: "/tmp/project",
		Duration:  12 * time.Millisecond,
		ExitCode:  &exitCode,
	})

	if bytes.Contains(output.Bytes(), []byte("echo retained")) {
		t.Fatalf("expected a status update to retain the unchanged input row")
	}
	if erases := bytes.Count(output.Bytes(), []byte("\x1b[2K")); erases != 1 {
		t.Fatalf("expected only the changed status row to repaint, got %d row erases", erases)
	}
	if !bytes.Contains(output.Bytes(), []byte("exit 0")) {
		t.Fatalf("expected the changed status row to render")
	}
}

func TestChatboxStatusRejectsOutOfOrderAsyncSnapshots(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 10)
	t.Cleanup(compositor.Close)
	compositor.SetStatusSnapshot(statusSnapshot{Revision: 2, Directory: "/new"})
	compositor.SetStatusSnapshot(statusSnapshot{Revision: 1, Directory: "/stale"})
	if got := compositor.statusSnapshot.Directory; got != "/new" {
		t.Fatalf("expected the newer status snapshot to win, got %q", got)
	}
}

func TestStatusDurationUsesCompactUnits(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{duration: 38 * time.Millisecond, want: "38ms"},
		{duration: 1400 * time.Millisecond, want: "1.4s"},
		{duration: 2*time.Minute + 13*time.Second, want: "2m13s"},
	} {
		if got := formatStatusDuration(test.duration); got != test.want {
			t.Fatalf("formatStatusDuration(%s)=%q, want %q", test.duration, got, test.want)
		}
	}
}

func TestGitStatusSegmentsKeepFactsIndependentlyStyleable(t *testing.T) {
	if unresolved := gitStatusSegments(gitStatusSnapshot{Branch: "main"}); len(unresolved) != 0 {
		t.Fatalf("expected unresolved Git state not to claim the repository is clean, got %+v", unresolved)
	}
	clean := gitStatusSegments(gitStatusSnapshot{Branch: "main", Resolved: true})
	if len(clean) != 1 || clean[0].name != "git-clean" || clean[0].text != "clean" {
		t.Fatalf("expected independently styled clean state, got %+v", clean)
	}
	segments := gitStatusSegments(gitStatusSnapshot{Operation: "rebasing", Conflicts: 1, Staged: 2, Modified: 3, Renamed: 1, Untracked: 4, Ahead: 2, Behind: 1})
	want := []string{"git-operation", "git-conflicts", "git-staged", "git-modified", "git-renamed", "git-untracked", "git-ahead", "git-behind"}
	for index, name := range want {
		if index >= len(segments) || segments[index].name != name {
			t.Fatalf("expected segment %d to be %q, got %+v", index, name, segments)
		}
	}
}

func TestBottomCompositorChatboxGrowsUpwardAndReportsDecoratedRows(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	var bottom bool
	var promptRows int
	compositor.SetTransientUIReflow(nil, func(nextBottom bool, nextRows int) {
		bottom = nextBottom
		promptRows = nextRows
	}, nil, nil, nil, nil)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("first\nsecond "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 8)
	if !bottom || promptRows != 4 {
		t.Fatalf("expected overlay geometry to reserve four decorated rows, got bottom=%v rows=%d", bottom, promptRows)
	}
	if got := screenLine(screen, 4); strings.TrimSpace(got) != "" {
		t.Fatalf("expected top padding above multiline input, got %q", got)
	}
	if got := screenLine(screen, 5); !strings.Contains(got, "first") {
		t.Fatalf("expected first content row inside chatbox, got %q", got)
	}
	if got := screenLine(screen, 6); !strings.Contains(got, "second") {
		t.Fatalf("expected final content row inside chatbox, got %q", got)
	}
	if got := screen.CursorPosition().Y; got != 6 {
		t.Fatalf("expected cursor to remain on final content row, got %d", got)
	}

	output.Reset()
	compositor.Resize(30, 10)
	resized := applyTerminalOutput(t, output.Bytes(), 30, 10)
	if got := screenLine(resized, 6); strings.TrimSpace(got) != "" {
		t.Fatalf("expected resized input padding to stay anchored at bottom, got %q", got)
	}
	if got := resized.CursorPosition().Y; got != 8 {
		t.Fatalf("expected resized cursor on final content row, got %d", got)
	}
}

func TestBottomCompositorChatboxCarriesExitStatusToTheNextPrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{Separator: " · ", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ false"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\r\nfailed\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:7"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 30, 8)
	if got := screenLine(screen, 7); !strings.Contains(got, "exit 7") {
		t.Fatalf("expected latest command status in chatbox footer, got %q", got)
	}
	if !terminalContainsLine(screen, "failed") {
		t.Fatalf("expected foreground output above the next chatbox, got %q", terminalScreenLines(screen))
	}
	if strings.ContainsAny(strings.Join(terminalScreenLines(screen), ""), "╭╮╰╯│├┤") {
		t.Fatalf("expected completed output and active prompt to remain borderless, got %q", terminalScreenLines(screen))
	}
}

func TestBottomCompositorStacksCompletedExecutionAboveNextPrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 16)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Scrollback: "snapshot",
		Separator:  " · ",
		Title:      terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:     terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
		Colors: map[string]string{
			"directory":     "#61ffcf",
			"duration-fast": "#61ffcf",
			"exit-success":  "#61ffcf",
		},
	})
	compositor.SetInputBoxPath("/tmp/project")
	now := time.Date(2026, time.July, 30, 15, 42, 0, 0, time.Local)
	compositor.now = func() time.Time { return now }
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ printf command-result"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	now = now.Add(125 * time.Millisecond)
	compositor.WritePTY([]byte("command-result\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 48, 16)
	lines := terminalScreenLines(screen)
	headerRow := terminalLineIndex(lines, "/tmp/project", 0)
	commandRow := terminalLineIndex(lines, "λ printf command-result", 0)
	resultRow := terminalLineIndex(lines, "command-result", commandRow+1)
	footerRow := terminalLineIndex(lines, "125ms", resultRow+1)
	nextPromptRow := terminalLineIndex(lines, "λ", resultRow+1)
	if headerRow < 0 || commandRow < 0 || resultRow < 0 || footerRow < 0 || nextPromptRow < 0 {
		t.Fatalf("expected completed execution above the next input surface, got %q", lines)
	}
	if !strings.Contains(lines[headerRow], "15:42") {
		t.Fatalf("expected completed execution header to retain its start time, got %q", lines[headerRow])
	}
	if headerRow >= commandRow || resultRow != commandRow+2 {
		t.Fatalf("expected completed command to retain one bottom padding row, got %q", lines)
	}
	if !strings.Contains(lines[footerRow], "exit 0") {
		t.Fatalf("expected completed execution footer to retain its outcome, got %q", lines[footerRow])
	}
	if nextPromptRow <= footerRow+1 {
		t.Fatalf("expected one blank row to separate completed and active executions, got %q", lines)
	}
	if got := lines[resultRow]; !strings.HasPrefix(got, strings.Repeat(" ", terminalInputHorizontalPadding)+"command-result") {
		t.Fatalf("expected command output to align with chatbox content, got %q", got)
	}
	for _, row := range []int{commandRow - 1, commandRow + 1} {
		cell := screen.CellAt(0, row)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("expected completed command padding at row %d, got %+v", row, cell)
		}
	}
	if strings.ContainsAny(strings.Join(lines, ""), "╭╮╰╯│├┤") {
		t.Fatalf("expected direct output without execution-card borders, got %q", lines)
	}
	completedCell := screen.CellAt(0, commandRow)
	activeCell := screen.CellAt(0, nextPromptRow)
	if completedCell == nil || completedCell.Style.Bg == nil || activeCell == nil || activeCell.Style.Bg == nil {
		t.Fatal("expected completed and active execution backgrounds")
	}
	completedRed, completedGreen, completedBlue, _ := completedCell.Style.Bg.RGBA()
	activeRed, activeGreen, activeBlue, _ := activeCell.Style.Bg.RGBA()
	if completedRed != 0x1717 || completedGreen != 0x1919 || completedBlue != 0x1d1d {
		t.Fatalf("expected completed command row to use #17191d, got #%04x%04x%04x", completedRed, completedGreen, completedBlue)
	}
	if completedRed == activeRed && completedGreen == activeGreen && completedBlue == activeBlue {
		t.Fatal("expected completed execution surface to be dimmer than the active surface")
	}
}

func TestBottomCompositorSnapshotsTitleAndStatusForCompletedExecution(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 120, 20)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Scrollback: "snapshot",
		Separator:  " · ",
		Title: terminalChatboxBarConfig{
			Left:  []string{"directory"},
			Right: []string{"versions"},
		},
		Status: terminalChatboxBarConfig{
			Left:  []string{"git-branch", "git-status"},
			Right: []string{"duration", "exit", "cpu", "memory"},
		},
		Colors: map[string]string{
			"directory":     "#61ffcf",
			"git-branch":    "#fd7df4",
			"git-modified":  "#f3c969",
			"go":            "#00add8",
			"duration-fast": "#61ffcf",
			"exit-success":  "#61ffcf",
			"load-low":      "#61ffcf",
		},
	})
	initialExit := 9
	compositor.SetStatusSnapshot(statusSnapshot{
		Revision:  1,
		Directory: "/tmp/project",
		Git:       gitStatusSnapshot{Branch: "main", Modified: 2},
		Versions:  map[string]string{"go": "1.26.0"},
		CPU:       8,
		Memory:    42,
		HasCPU:    true,
		HasMemory: true,
		Duration:  3 * time.Second,
		ExitCode:  &initialExit,
	})
	now := time.Date(2026, time.July, 30, 15, 42, 0, 0, time.Local)
	compositor.now = func() time.Time { return now }
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ go test ./..."))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))

	laterExit := 7
	compositor.SetStatusSnapshot(statusSnapshot{
		Revision:  2,
		Directory: "/tmp/later",
		Git:       gitStatusSnapshot{Branch: "changed", Modified: 99},
		Versions:  map[string]string{"go": "1.27.0"},
		CPU:       99,
		Memory:    98,
		HasCPU:    true,
		HasMemory: true,
		Duration:  time.Minute,
		ExitCode:  &laterExit,
	})
	now = now.Add(125 * time.Millisecond)
	compositor.WritePTY([]byte("ok\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))

	screen := applyTerminalOutput(t, output.Bytes(), 120, 20)
	lines := terminalScreenLines(screen)
	rendered := strings.Join(lines, "\n")
	for _, expected := range []string{
		"/tmp/project",
		"15:42",
		"Go 1.26.0",
		"λ go test ./...",
		"ok",
		"main",
		"modified 2",
		"125ms",
		"exit 0",
		"CPU 8%",
		"RAM 42%",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected completed execution snapshot to contain %q, got %q", expected, lines)
		}
	}
	for _, stale := range []string{
		"/tmp/later",
		"changed",
		"modified 99",
		"Go 1.27.0",
		"exit 7",
		"CPU 99%",
		"RAM 98%",
	} {
		if strings.Contains(rendered, stale) {
			t.Fatalf("expected completed execution snapshot to exclude later state %q, got %q", stale, lines)
		}
	}
	titleRow := terminalLineIndex(lines, "/tmp/project", 0)
	commandRow := terminalLineIndex(lines, "λ go test ./...", titleRow+1)
	contextRow := terminalLineIndex(lines, "modified 2", commandRow+1)
	outputRow := terminalLineIndex(lines, "ok", contextRow+1)
	outcomeRow := terminalLineIndex(lines, "125ms", outputRow+1)
	if titleRow < 0 || commandRow < 0 || contextRow < 0 || outputRow < 0 || outcomeRow < 0 {
		t.Fatalf("expected complete historical execution layout, got %q", lines)
	}
	if !(titleRow < commandRow && commandRow < contextRow && contextRow < outputRow && outputRow < outcomeRow) {
		t.Fatalf(
			"expected title, chatbox, context, output, and outcome order; got rows %d, %d, %d, %d, %d",
			titleRow,
			commandRow,
			contextRow,
			outputRow,
			outcomeRow,
		)
	}
	for _, row := range []int{titleRow, contextRow} {
		cell := screen.CellAt(0, row)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("expected historical metadata background at row %d, got %+v", row, cell)
		}
		red, green, blue, _ := cell.Style.Bg.RGBA()
		if red != 0x0808 || green != 0x0a0a || blue != 0x0d0d {
			t.Fatalf(
				"expected historical metadata row %d to use #080a0d, got #%04x%04x%04x",
				row,
				red,
				green,
				blue,
			)
		}
	}
	for _, row := range []int{commandRow, outcomeRow} {
		cell := screen.CellAt(0, row)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("expected historical grouping background at row %d, got %+v", row, cell)
		}
		red, green, blue, _ := cell.Style.Bg.RGBA()
		if red != 0x1717 || green != 0x1919 || blue != 0x1d1d {
			t.Fatalf(
				"expected historical grouping row %d to use #17191d, got #%04x%04x%04x",
				row,
				red,
				green,
				blue,
			)
		}
	}
}

func TestBottomCompositorOutputScrollbackKeepsSilentCommandVisibleUntilOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 16)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Scrollback: "output",
		Separator:  " · ",
		Title:      terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:     terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ printf plain-output"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	output.Reset()
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	transition := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 16))
	rendered := strings.Join(transition, "\n")
	if !strings.Contains(rendered, "λ printf plain-output") {
		t.Fatalf("expected a silent running command to remain visible, got %q", transition)
	}
	if !strings.Contains(rendered, "/tmp/project") || !strings.Contains(rendered, "exit") {
		t.Fatalf("expected the running command to retain its title and status, got %q", transition)
	}
	runningRow := terminalLineIndex(transition, "λ printf plain-output", 0)
	runningScreen := applyTerminalOutput(t, output.Bytes(), 48, 16)
	runningCell := runningScreen.CellAt(0, runningRow)
	if runningCell == nil || runningCell.Style.Bg == nil {
		t.Fatalf("expected the running command to retain a dimmed surface, got %+v", runningCell)
	}

	output.Reset()
	compositor.WritePTY([]byte("plain-output\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	if got := output.String(); !strings.HasSuffix(got, "plain-output\r\n") {
		t.Fatalf("expected output-only scrollback to preserve raw command output, got %q", got)
	}

	output.Reset()
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	if prompt := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 16)); terminalLineIndex(prompt, "λ", 0) < 0 {
		t.Fatalf("expected active chatbox to return after raw output, got %q", prompt)
	}
}

func TestBottomCompositorOutputScrollbackClearsRunningCardAfterSilentCompletion(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 12)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Scrollback: "output",
		Prompt:     "λ ",
	})
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ go clean -cache"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	if running := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 12)); terminalLineIndex(running, "go clean -cache", 0) < 0 {
		t.Fatalf("expected the silent command to remain visible while running, got %q", running)
	}

	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	completed := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 12))
	if strings.Contains(strings.Join(completed, "\n"), "go clean -cache") {
		t.Fatalf("expected output-only history to discard the completed silent command, got %q", completed)
	}
	if terminalLineIndex(completed, "λ", 0) < 0 {
		t.Fatalf("expected the next prompt after silent completion, got %q", completed)
	}
}

func TestBottomCompositorOutputScrollbackSeparatesCompletedExecutions(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 16)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Scrollback:     "output",
		HistorySpacing: 1,
		Prompt:         "λ ",
	})
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ true"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()
	compositor.WritePTY([]byte("unterminated"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))

	if !strings.HasSuffix(output.String(), "unterminated\r\n\r\n") {
		t.Fatalf("expected an open output line and one blank separator before the next prompt, got %q", output.String())
	}
}

func TestBottomCompositorRetainsExecutionFooterAndGapAfterSplitOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 16)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator:      " · ",
		HistorySpacing: 1,
		Title:          terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:         terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	now := time.Date(2026, time.July, 30, 15, 42, 0, 0, time.Local)
	compositor.now = func() time.Time { return now }
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ split"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("split"))
	compositor.WritePTY([]byte(" output\r\n"))
	now = now.Add(80 * time.Millisecond)
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 16))
	resultRow := terminalLineIndex(lines, "split output", 0)
	footerRow := terminalLineIndex(lines, "80ms", resultRow+1)
	nextPromptRow := terminalLineIndex(lines, "λ", footerRow+1)
	if resultRow < 0 || footerRow < 0 || nextPromptRow <= footerRow+1 {
		t.Fatalf("expected split command output, completion footer, and execution gap, got %q", lines)
	}
}

func TestBottomCompositorRetainsExecutionFooterAfterApplicationControlTraffic(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 48, 16)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title:     terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:    terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	now := time.Date(2026, time.July, 30, 15, 42, 0, 0, time.Local)
	compositor.now = func() time.Time { return now }
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ controlled"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\r\nresult\r\n"))
	compositor.WritePTY([]byte("\x1b[?25l\x1b[?25h"))
	now = now.Add(90 * time.Millisecond)
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 48, 16))
	resultRow := terminalLineIndex(lines, "result", 0)
	footerRow := terminalLineIndex(lines, "90ms", resultRow+1)
	nextPromptRow := terminalLineIndex(lines, "λ", footerRow+1)
	if resultRow < 0 || footerRow < 0 || nextPromptRow <= footerRow+1 {
		t.Fatalf("expected control traffic to preserve the execution footer and gap, got %q", lines)
	}
}

func TestBottomCompositorTransitionsPromptWithOneErasePerSurfaceRow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 80, 12)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title:     terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:    terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ ls"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	surfaceRows := compositor.surfaceRows
	output.Reset()

	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))

	if erases := bytes.Count(output.Bytes(), []byte("\x1b[2K")); erases > surfaceRows {
		t.Fatalf("expected at most one erase per transitioned surface row, got %d erases for %d rows", erases, surfaceRows)
	}
}

func TestBottomCompositorFirstDrawErasesEachSurfaceRowAtMostOnce(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 80, 12)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title:     terminalChatboxBarConfig{Left: []string{"directory"}},
		Status:    terminalChatboxBarConfig{Right: []string{"duration", "exit"}},
	})
	compositor.SetInputBoxPath("/tmp/project")
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	if erases := bytes.Count(output.Bytes(), []byte("\x1b[2K")); erases > compositor.surfaceRows {
		t.Fatalf("expected at most one erase per first-draw surface row, got %d erases for %d rows", erases, compositor.surfaceRows)
	}
}

func TestBottomCompositorPreservesCompletedOutputWhenNextInputGrowsUpward(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 32, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ first"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\r\nresult\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ next"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "continuation-start"))
	compositor.WritePTY([]byte("> growing"))
	compositor.WritePTY(terminalMarkerBytes(marker, "continuation-end"))

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 32, 10))
	resultRow := terminalLineIndex(lines, "result", 0)
	nextPromptRow := terminalLineIndex(lines, "λ next", resultRow+1)
	continuationRow := terminalLineIndex(lines, "> growing", nextPromptRow+1)
	if resultRow < 0 || nextPromptRow < 0 || continuationRow < 0 {
		t.Fatalf("expected completed output to survive upward input growth, got %q", lines)
	}
}

func TestBottomCompositorDoesNotExposeDestructiveMultilinePromptRedraw(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 32, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ first"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\r\nresult\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ next"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	compositor.WritePTY([]byte("\r\x1b[H\x1b[2J"))
	compositor.WritePTY([]byte("context\r\nλ nextx"))

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 32, 10))
	for _, expected := range []string{"result", "λ nextx"} {
		if terminalLineIndex(lines, expected, 0) < 0 {
			t.Fatalf("expected atomic prompt redraw to retain %q, got %q", expected, lines)
		}
	}
}

func TestBottomCompositorWaitsForAnOrderedPromptBoundaryBeforeAdvertisingLayout(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	if !compositor.Managed() {
		t.Fatal("expected valid bottom configuration to advertise managed capability before the first prompt")
	}
	if compositor.Enabled() {
		t.Fatal("expected overlays to remain in classic geometry before the first prompt marker")
	}
	compositor.WritePTY([]byte("unmarked shell startup\r\n"))
	if compositor.Enabled() {
		t.Fatal("expected unmarked output to leave bottom overlays disabled")
	}

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	if !compositor.Enabled() {
		t.Fatal("expected bottom layout after the first complete ordered prompt")
	}
}

func TestBottomCompositorGrowsMultilineInputUpwardWithoutMovingFinalRow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 18, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("first\nsecond "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := vt.NewEmulator(18, 6)
	t.Cleanup(func() { _ = screen.Close() })
	_, _ = screen.Write(output.Bytes())

	if got := screenLine(screen, 4); !strings.Contains(got, "first") {
		t.Fatalf("expected first prompt row above the input, got %q", got)
	}
	if got := screenLine(screen, 5); !strings.Contains(got, "second") {
		t.Fatalf("expected final prompt row at bottom, got %q", got)
	}
	if got := screen.CursorPosition().Y; got != 5 {
		t.Fatalf("expected cursor to remain on last row, got %d", got)
	}
}

func TestBottomCompositorGrowsForMultilinePasteAfterPromptActivation(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.SetMultilineInput(true)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("first command\r\nsecond command"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 8)
	if compositor.PromptRows() != 4 {
		t.Fatalf("expected multiline paste to reserve four decorated rows, got %d", compositor.PromptRows())
	}
	if got := screenLine(screen, 5); !strings.Contains(got, "first command") {
		t.Fatalf("expected first pasted row inside chatbox, got %q", got)
	}
	if got := screenLine(screen, 6); !strings.Contains(got, "second command") {
		t.Fatalf("expected final pasted row inside chatbox, got %q", got)
	}
}

func TestBottomCompositorKeepsContinuationPromptAboveFinalInputRow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("echo '"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "continuation-start"))
	compositor.WritePTY([]byte("> "))
	compositor.WritePTY(terminalMarkerBytes(marker, "continuation-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := screenLine(screen, 4); !strings.Contains(got, "λ echo '") {
		t.Fatalf("expected primary input above continuation prompt, got %q", got)
	}
	if got := screenLine(screen, 5); !strings.Contains(got, ">") {
		t.Fatalf("expected continuation input on last row, got %q", got)
	}
}

func TestBottomCompositorReturnsRepeatedCommandsToBottom(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 7)
	t.Cleanup(compositor.Close)

	for _, command := range []string{"first", "second"} {
		compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
		compositor.WritePTY([]byte("λ "))
		compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
		compositor.WritePTY([]byte(command))
		compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
		compositor.WritePTY([]byte("\r\nresult:" + command + "\r\n"))
		compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	}
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 7)
	if got := screen.CursorPosition().Y; got != 6 {
		t.Fatalf("expected final prompt on last row, got %d", got)
	}
	if got := screenLine(screen, 6); !strings.Contains(got, "λ") {
		t.Fatalf("expected final prompt at bottom, got %q", got)
	}
}

func TestBottomCompositorKeepsEditingOnSameVisualRow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("abcd"))
	first := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := first.CursorPosition().Y; got != 5 {
		t.Fatalf("expected initial edit on last row, got %d", got)
	}

	compositor.WritePTY([]byte("\b\bX"))
	edited := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := edited.CursorPosition().Y; got != 5 {
		t.Fatalf("expected cursor movement to stay on last row, got %d", got)
	}
	if got := screenLine(edited, 5); !strings.Contains(got, "λ abXd") {
		t.Fatalf("expected edited input on bottom row, got %q", got)
	}
}

func TestBottomCompositorPreservesUnicodeCellGeometry(t *testing.T) {
	const marker = "test-session"
	input := []byte("界 e\u0301 ")
	for split := 0; split <= len(input); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			var output bytes.Buffer
			compositor := newTerminalCompositor(&output, "bottom", marker, 20, 5)
			t.Cleanup(compositor.Close)

			compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
			compositor.WritePTY(input[:split])
			compositor.WritePTY(input[split:])
			compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
			compositor.WritePTY([]byte("λ"))

			screen := applyTerminalOutput(t, output.Bytes(), 20, 5)
			if got := screenLine(screen, 4); !strings.Contains(got, "界 e\u0301 λ") {
				t.Fatalf("expected wide and combining characters to survive, got %q", got)
			}
			if got := screen.CursorPosition().Y; got != 4 {
				t.Fatalf("expected Unicode input cursor on last row, got %d", got)
			}
		})
	}
}

func TestBottomCompositorAnchorsWrappedInputUpward(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 10, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("1234567890"))

	screen := applyTerminalOutput(t, output.Bytes(), 10, 5)
	if got := screen.CursorPosition().Y; got != 4 {
		t.Fatalf("expected wrapped input cursor on last row, got %d", got)
	}
	if got := screenLine(screen, 3); !strings.Contains(got, "λ 12345678") {
		t.Fatalf("expected first wrapped row above final input, got %q", got)
	}
	if got := screenLine(screen, 4); !strings.Contains(got, "90") {
		t.Fatalf("expected wrapped suffix on final row, got %q", got)
	}
}

func TestBottomCompositorPreservesStyledRightPrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("\x1b[32mλ\x1b[0m \x1b7\x1b[1;17H\x1b[35mRP\x1b[0m\x1b8"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 5)
	line := screenLine(screen, 4)
	if !strings.Contains(line, "λ") || !strings.Contains(line, "RP") {
		t.Fatalf("expected left and right prompt content on bottom row, got %q", line)
	}
	if got := screen.CursorPosition(); got.Y != 4 || got.X != 2 {
		t.Fatalf("expected cursor restored after right prompt, got %+v", got)
	}
}

func TestBottomCompositorSeparatesOutputWithoutTrailingNewlineFromNextPrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("partial output"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 6)
	if got := screenLine(screen, 5); strings.Contains(got, "partial output") || !strings.Contains(got, "λ") {
		t.Fatalf("expected only the prompt on the bottom row, got %q", got)
	}
	if !terminalContainsLine(screen, "partial output") {
		t.Fatal("expected unterminated output to remain visible above the prompt")
	}
}

func TestBottomCompositorRepinsAfterClearScreen(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("before"))
	compositor.WritePTY([]byte("\x1b[H\x1b[2Jλ after"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := screenLine(screen, 5); !strings.Contains(got, "λ after") {
		t.Fatalf("expected Ctrl-L redraw on the bottom row, got %q", got)
	}
	if got := screen.CursorPosition().Y; got != 5 {
		t.Fatalf("expected clear-screen cursor on last row, got %d", got)
	}
}

func TestBottomCompositorDoesNotExposeSplitPromptRedrawPrelude(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ old"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 6)
	output.Reset()
	compositor.WritePTY([]byte("\r"))
	_, _ = screen.Write(output.Bytes())
	output.Reset()
	compositor.WritePTY([]byte("\x1b["))
	compositor.WritePTY([]byte("2K"))
	if output.Len() != 0 {
		t.Fatalf("expected redraw prelude to remain atomic with the next prompt, got %q", output.Bytes())
	}
	if got := screenLine(screen, 5); got != "λ old" {
		t.Fatalf("expected active prompt to remain visible until replacement is complete, got %q", got)
	}

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ new"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	_, _ = screen.Write(output.Bytes())
	if got := screenLine(screen, 5); got != "λ new" {
		t.Fatalf("expected replacement prompt to appear atomically at the bottom, got %q", got)
	}
}

func TestBottomCompositorStartsFreshPromptAfterCancel(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ cancelled"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("^C\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := screenLine(screen, 5); got != "λ" {
		t.Fatalf("expected a clean prompt after cancellation, got %q", got)
	}
}

func TestBottomCompositorKeepsBottomPositionAfterClearInputAndEmptySubmit(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ temporary"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\x1b[2Kλ "))

	cleared := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := screenLine(cleared, 5); got != "λ" {
		t.Fatalf("expected Ctrl-U redraw on bottom row, got %q", got)
	}

	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	submitted := applyTerminalOutput(t, output.Bytes(), 20, 6)
	if got := screenLine(submitted, 5); got != "λ" {
		t.Fatalf("expected empty submission to return to bottom prompt, got %q", got)
	}
}

func TestBottomCompositorDoesNotScrollOutputTwiceForSubmittedLineBreak(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 6)
	t.Cleanup(compositor.Close)

	compositor.WritePTY([]byte("one\r\ntwo\r\nthree\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ echo"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("result\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 6)
	for _, expected := range []string{"three", "λ echo", "result"} {
		if !terminalContainsLine(screen, expected) {
			t.Fatalf("expected %q to remain in the live output without an extra scroll", expected)
		}
	}
	if got := screenLine(screen, 5); got != "λ" {
		t.Fatalf("expected next prompt on the bottom row, got %q", got)
	}
}

func TestBottomCompositorRendersBackgroundOutputAboveActivePrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 7)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ active"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("background notice\r\n"))
	compositor.WritePTY([]byte("!"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 7)
	if got := screenLine(screen, 6); !strings.Contains(got, "λ active!") {
		t.Fatalf("expected active prompt to remain fixed after background output, got %q", got)
	}
	if !terminalContainsLine(screen, "background notice") {
		t.Fatal("expected background output above active prompt")
	}
}

func TestBottomCompositorReflowsTransientUIAroundBackgroundOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 7)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ active"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	visible := true
	clearOverlay := func() []byte {
		return []byte("\x1b7\x1b[3;1H\x1b[2K\x1b[4;1H\x1b[2K\x1b8")
	}
	renderOverlay := func() []byte {
		if !visible {
			return nil
		}
		return []byte("\x1b7\x1b[3;1Hmenu-top\x1b[4;1Hmenu-body\x1b8")
	}
	compositor.SetTransientUIReflow(clearOverlay, func(bool, int) {}, renderOverlay, func() []byte {
		visible = false
		return clearOverlay()
	}, func() (int, int) { return 2, 2 }, func() bool { return visible })
	compositor.ComposeUI(renderOverlay)

	compositor.WritePTY([]byte("background notice\r\n"))
	visible = false
	compositor.ComposeUI(clearOverlay)

	screen := applyTerminalOutput(t, output.Bytes(), 24, 7)
	if terminalContainsLine(screen, "menu-") {
		t.Fatal("expected background output followed by close to leave no shifted overlay rows")
	}
	if !terminalContainsLine(screen, "background notice") {
		t.Fatal("expected background output to remain above the prompt")
	}
	if got := screenLine(screen, 6); got != "λ active" {
		t.Fatalf("expected prompt to remain fixed while transient UI reflows, got %q", got)
	}
}

func TestBottomCompositorRestoresCompletedCardsAfterTransientMenuCloses(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 32, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ first"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\r\nresult\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ next"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	visible := false
	menuTop, menuRows := 0, 0
	compositor.SetTransientUIReflow(nil, nil, nil, nil, func() (int, int) {
		return menuTop, menuRows
	}, func() bool { return visible })
	compositor.ComposeUI(func() []byte {
		visible = true
		menuTop, menuRows = 1, 4
		return []byte("\x1b[2;1H\x1b[2Kmenu\x1b[3;1H\x1b[2Kitem\x1b[4;1H\x1b[2Kitem\x1b[5;1H\x1b[2Kfooter")
	})
	compositor.ComposeUI(func() []byte {
		visible = false
		var clear strings.Builder
		for row := menuTop; row < menuTop+menuRows; row++ {
			fmt.Fprintf(&clear, "\x1b[%d;1H\x1b[2K", row+1)
		}
		return []byte(clear.String())
	})

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 32, 10))
	for _, expected := range []string{"λ first", "result", "λ next"} {
		if terminalLineIndex(lines, expected, 0) < 0 {
			t.Fatalf("expected closing transient menu to restore %q, got %q", expected, lines)
		}
	}
}

func TestTerminalUIPresenterOrdersRenderBeforeLaterClear(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "classic", "", 20, 5)
	t.Cleanup(compositor.Close)
	presenter := newTerminalUIPresenter(compositor.ComposeUI)

	renderStarted := make(chan struct{})
	releaseRender := make(chan struct{})
	renderDone := make(chan struct{})
	go func() {
		defer close(renderDone)
		presenter.Present(func() []byte {
			close(renderStarted)
			<-releaseRender
			return []byte("\x1b[1;1Hstale menu")
		})
	}()

	<-renderStarted
	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		presenter.Present(func() []byte { return []byte("\x1b[1;1H\x1b[2K") })
	}()
	close(releaseRender)
	<-renderDone
	<-clearDone

	screen := applyTerminalOutput(t, output.Bytes(), 20, 5)
	if terminalContainsLine(screen, "stale menu") {
		t.Fatal("expected a later clear to be presented after the in-flight render")
	}
}

func TestBottomCompositorClearsTransientUIAtOrderedCommandBoundary(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 7)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ active"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	visible := true
	clearOverlay := func() []byte {
		if !visible {
			return nil
		}
		return []byte("\x1b7\x1b[3;1H\x1b[2K\x1b[4;1H\x1b[2K\x1b8")
	}
	renderOverlay := func() []byte {
		if !visible {
			return nil
		}
		return []byte("\x1b7\x1b[3;1Hmenu-top\x1b[4;1Hmenu-body\x1b8")
	}
	compositor.SetTransientUIReflow(clearOverlay, func(bool, int) {}, renderOverlay, func() []byte {
		cleared := clearOverlay()
		visible = false
		return cleared
	}, nil, func() bool { return visible })
	compositor.ComposeUI(renderOverlay)
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))

	screen := applyTerminalOutput(t, output.Bytes(), 24, 7)
	if terminalContainsLine(screen, "menu-") {
		t.Fatal("expected the PTY command boundary to clear transient UI before foreground output")
	}
	if visible {
		t.Fatal("expected the ordered command boundary to disable transient UI state")
	}
}

func TestBottomCompositorRepinsIdlePromptAfterResize(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 18, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ resize"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	output.Reset()

	compositor.Resize(24, 8)
	screen := applyTerminalOutput(t, output.Bytes(), 24, 8)
	if got := screen.CursorPosition().Y; got != 7 {
		t.Fatalf("expected resized prompt cursor on last row, got %d", got)
	}
	if got := screenLine(screen, 7); !strings.Contains(got, "λ resize") {
		t.Fatalf("expected prompt after resize, got %q", got)
	}
}

func TestBottomCompositorResizeDuringExecutionDoesNotComposeOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 18, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()
	compositor.Resize(24, 8)
	compositor.WritePTY([]byte("streamed"))

	if got := output.String(); got != "streamed" {
		t.Fatalf("expected resize during execution to leave streaming output untouched, got %q", got)
	}
}

func TestBottomCompositorRejectsScrollMarginsBeyondModelHeight(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 40)
	t.Cleanup(compositor.Close)

	compositor.WritePTY([]byte("\x1b[1;39r"))
	compositor.Resize(80, 34)
	compositor.WritePTY([]byte("\x1b[38S"))
	compositor.WritePTY([]byte("still alive"))

	if got := compositor.emulator.Height(); got != 34 {
		t.Fatalf("expected model height 34 after invalid scroll margins, got %d", got)
	}
	if compositor.modelRecoveries != 0 {
		t.Fatalf("expected resize to normalize stale margins before emulator recovery, got %d recoveries", compositor.modelRecoveries)
	}
}

func TestModelMarginSanitizerClampsSplitVerticalAndHorizontalSequences(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 34)
	t.Cleanup(compositor.Close)

	if got := compositor.sanitizeModelCSI([]byte("\x1b[1;")); len(got) != 0 {
		t.Fatalf("expected split CSI prefix to be buffered, got %q", got)
	}
	if got := string(compositor.sanitizeModelCSI([]byte("39r"))); got != "\x1b[1;34r" {
		t.Fatalf("expected vertical margin clamp, got %q", got)
	}
	if got := string(compositor.sanitizeModelCSI([]byte("\x1b[?69h\x1b[1;120s"))); got != "\x1b[?69h\x1b[1;80s" {
		t.Fatalf("expected horizontal margin clamp, got %q", got)
	}
}

func TestModelFailureResetsTransientStateAndWaitsForNextPrompt(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 34)
	t.Cleanup(compositor.Close)
	compositor.closeEmulator()
	compositor.phase = terminalInput
	compositor.awaitingPrompt = false
	compositor.surfaceRows = 4
	compositor.surfaceContentRows = 2
	compositor.surfaceContentLines = []string{"stale"}
	compositor.pendingRedraw = []byte("stale")
	compositor.commandCardOpen = true
	compositor.commandStartedAt = time.Now()
	compositor.commandDirectory = "/tmp/stale"

	compositor.writeModel([]byte("trigger model failure"))

	if compositor.modelRecoveries != 1 {
		t.Fatalf("expected one contained model recovery, got %d", compositor.modelRecoveries)
	}
	if compositor.emulator == nil {
		t.Fatal("expected a fresh emulator after model recovery")
	}
	if !compositor.awaitingPrompt || compositor.phase != terminalOutput {
		t.Fatal("expected recovery to wait for the next prompt boundary")
	}
	if compositor.surfaceRows != 0 || compositor.surfaceContentRows != 0 || len(compositor.pendingRedraw) != 0 {
		t.Fatal("expected recovery to discard stale transient geometry")
	}
	if compositor.commandCardOpen || !compositor.commandStartedAt.IsZero() || compositor.commandDirectory != "" {
		t.Fatal("expected recovery to discard stale command-card state")
	}
}

func TestBottomCompositorUsesConfiguredStatusThresholds(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 12)
	t.Cleanup(compositor.Close)
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Metrics:      "when-high",
		DurationFast: 100 * time.Millisecond,
		DurationSlow: time.Second,
		CPUAverage:   25, CPUHigh: 50, CPUCritical: 75,
		MemoryAverage: 40, MemoryHigh: 60, MemoryCritical: 80,
	})

	if got := compositor.durationStatusColor(500 * time.Millisecond); got != "duration-average" {
		t.Fatalf("expected configured average duration, got %q", got)
	}
	if got := compositor.loadStatusColor("cpu", 80); got != "cpu-critical" {
		t.Fatalf("expected configured critical CPU, got %q", got)
	}
	if got := compositor.loadStatusColor("memory", 70); got != "memory-high" {
		t.Fatalf("expected configured high memory, got %q", got)
	}
	if compositor.metricVisible(49, 50) || !compositor.metricVisible(50, 50) {
		t.Fatal("expected when-high metrics to appear only at the configured high threshold")
	}
}

func TestBottomCompositorFiltersVersionsAndContexts(t *testing.T) {
	compositor := newTerminalCompositor(io.Discard, "bottom", "test-session", 80, 12)
	t.Cleanup(compositor.Close)
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Versions: "auto", VersionAllow: []string{"go"}, VersionDeny: []string{"php"},
		KubernetesContext: "never", AWSContext: "always", DockerContext: "auto",
	})
	if !compositor.versionVisible("go") || compositor.versionVisible("php") || compositor.versionVisible("rust") {
		t.Fatal("expected version allow and deny policy to be enforced")
	}
	contexts := compositor.visibleContexts("git status", operationalContextSnapshot{
		Kubernetes: "prod", AWSProfile: "staging", AWSRegion: "eu-north-1", Docker: "desktop",
	})
	if got := strings.Join(contexts, " "); got != "AWS staging eu-north-1" {
		t.Fatalf("expected only always-visible AWS context, got %q", got)
	}
}

func TestSnapshotMetadataChangedIgnoresOutcomeAndMetrics(t *testing.T) {
	left := statusSnapshot{
		Directory: "/tmp/project", Git: gitStatusSnapshot{Branch: "main"},
		Versions: map[string]string{"go": "1.26.0"}, CPU: 10, Memory: 20, Duration: time.Second,
	}
	right := cloneStatusSnapshot(left)
	right.CPU = 95
	right.Memory = 80
	right.Duration = 5 * time.Second
	exit := 1
	right.ExitCode = &exit
	if !statusSnapshotMetadataEqual(left, right) {
		t.Fatal("expected outcome and metrics to be excluded from metadata comparison")
	}
	right.Directory = "/tmp/other"
	if statusSnapshotMetadataEqual(left, right) {
		t.Fatal("expected directory change to make snapshot metadata visible")
	}
}

func TestBottomCompositorDropsCompletedCardStateWhenLayoutIsSuspended(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 24, 7)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ running"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.Resize(24, 1)
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.Resize(24, 7)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ resumed"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	output.Reset()

	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	if output.Len() != 0 {
		t.Fatalf("expected no stale completed-card footer after layout suspension, got %q", output.Bytes())
	}
}

func TestBottomCompositorSafelySuspendsAndResumesAcrossUnusableResize(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 18, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.Resize(18, 1)
	compositor.WritePTY([]byte("classic fallback"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))

	if bytes.Contains(output.Bytes(), []byte("777;vuja")) {
		t.Fatal("expected markers to remain private while bottom layout is suspended")
	}
	if !strings.Contains(output.String(), "classic fallback") {
		t.Fatal("expected output to pass through while terminal geometry is unusable")
	}

	compositor.Resize(18, 5)
	if compositor.Enabled() {
		t.Fatal("expected bottom overlays to remain disabled until the next complete prompt boundary")
	}
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ resumed"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	if !compositor.Enabled() {
		t.Fatal("expected bottom layout to resume at the next prompt")
	}

	screen := applyTerminalOutput(t, output.Bytes(), 18, 5)
	if got := screenLine(screen, 4); !strings.Contains(got, "λ resumed") {
		t.Fatalf("expected resumed prompt on bottom row, got %q", got)
	}
}

func TestBottomCompositorRelinquishesAlternateScreen(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ app"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("\x1b[?1049h\x1b[Hfull screen"))

	active := applyTerminalOutput(t, output.Bytes(), 30, 8)
	if got := screenLine(active, 0); !strings.Contains(got, "full screen") {
		t.Fatalf("expected alternate-screen application output, got %q", got)
	}

	compositor.WritePTY([]byte("\x1b[?1049l"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	returned := applyTerminalOutput(t, output.Bytes(), 30, 8)
	if got := returned.CursorPosition().Y; got != 6 {
		t.Fatalf("expected prompt to return to bottom after alternate screen, got %d", got)
	}
}

func TestBottomCompositorPadsLineOrientedForegroundOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ stream"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()

	for range 100 {
		chunk := []byte("0123456789\r\n")
		compositor.WritePTY(chunk)
	}

	if got := strings.Count(output.String(), terminalSyncStart); got != 0 {
		t.Fatalf("expected foreground output to stream directly, got %d composed frames", got)
	}
	if got := strings.Count(output.String(), "0123456789"); got != 100 {
		t.Fatalf("expected every streamed output chunk exactly once, got %d", got)
	}
	screen := applyTerminalOutput(t, output.Bytes(), 30, 110)
	for row := 0; row < 100; row++ {
		line := screenLine(screen, row)
		if !strings.HasPrefix(line, "  0123456789") || strings.Contains(line, "│") {
			t.Fatalf("expected streamed row %d with chatbox-aligned padding, got %q", row, line)
		}
	}
}

func TestBottomCompositorReservesMatchingRightPaddingForLineOutput(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ output"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()
	compositor.WritePTY([]byte("abcdefghijklmnopqr\r\n"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 4)
	if got := screenLine(screen, 0); got != "  abcdefghijklmnop" {
		t.Fatalf("expected content to wrap before the two-column right inset, got %q", got)
	}
	if got := screenLine(screen, 1); !strings.HasPrefix(got, "  qr") {
		t.Fatalf("expected wrapped output to retain the left inset, got %q", got)
	}
}

func TestBottomCompositorKeepsSplitAndWrappedOutputInDirectTerminalFlow(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ output"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()

	compositor.WritePTY([]byte("abcdefghij"))
	if bytes.Contains(output.Bytes(), []byte("│")) || !bytes.Contains(output.Bytes(), []byte("abcdefghij")) {
		t.Fatalf("expected partial output immediately without a border, got %q", output.Bytes())
	}
	compositor.WritePTY([]byte("klmnopqrstuvwxyz\r\n"))

	screen := applyTerminalOutput(t, output.Bytes(), 20, 4)
	for row, fragment := range []string{"abcdefghijklmnopqrst", "uvwxyz"} {
		line := screenLine(screen, row)
		if !strings.HasPrefix(line, fragment) || strings.Contains(line, "│") {
			t.Fatalf("expected wrapped row %d without side borders, got %q", row, line)
		}
	}
}

func TestBottomCompositorPassesForegroundInteractiveTrafficThroughExactly(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ ssh host"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()

	passwordPrompt := []byte("Password: ")
	compositor.WritePTY(passwordPrompt)
	if !bytes.Equal(output.Bytes(), passwordPrompt) {
		t.Fatalf("expected an unterminated password prompt unchanged, got %q", output.Bytes())
	}
	output.Reset()

	interactive := []byte(
		"\x1b[?2004hPassword: " +
			"\x1b[6n" +
			"\x1b[?1000h\x1b[?1006h" +
			"\x1b[2K\rretry: " +
			"\x1b[?1006l\x1b[?1000l\x1b[?2004l",
	)
	compositor.WritePTY(interactive)

	if !bytes.Equal(output.Bytes(), interactive) {
		t.Fatalf("expected foreground interactive traffic unchanged, got %q", output.Bytes())
	}
}

func TestBottomCompositorConsumesModelRepliesWithoutBlockingPTYQueries(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\x1b[5n\x1b[6n\x1b[?6n"))

	screen := applyTerminalOutput(t, output.Bytes(), 30, 8)
	if got := screen.CursorPosition().Y; got != 7 {
		t.Fatalf("expected prompt to remain usable after terminal queries, got row %d", got)
	}

	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()
	query := []byte("\x1b[5n\x1b[6n\x1b[?6n")
	compositor.WritePTY(query)
	if !bytes.Equal(output.Bytes(), query) {
		t.Fatalf("expected foreground terminal queries unchanged, got %q", output.Bytes())
	}
}

func TestBottomCompositorKeepsUpdateNoticeAboveActivePrompt(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 40, 8)
	t.Cleanup(compositor.Close)

	compositor.WritePTY([]byte("command output\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WriteNotification(updateNotice("v9.9.9"))

	screen := applyTerminalOutput(t, output.Bytes(), 40, 8)
	if !terminalContainsLine(screen, "new version") {
		t.Fatal("expected update notice above the active prompt")
	}
	if got := screenLine(screen, 7); got != "λ" {
		t.Fatalf("expected prompt to remain pinned after update notice, got %q", got)
	}
}

func TestBottomCompositorPresentsOverlayWritesAsCompleteFrames(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	t.Cleanup(compositor.Close)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	output.Reset()

	overlay := []byte("\x1b7menu\x1b8")
	compositor.WriteUI(overlay)
	if got := output.String(); got != terminalSyncStart+string(overlay)+terminalSyncEnd {
		t.Fatalf("expected one synchronized overlay frame, got %q", got)
	}
}

func TestBottomCompositorRejectsLateOverlayUpdatesOutsideInputPhase(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 30, 8)
	t.Cleanup(compositor.Close)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ command"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	output.Reset()

	rendered := false
	compositor.ComposeUI(func() []byte {
		rendered = true
		return []byte("late overlay")
	})
	if rendered {
		t.Fatal("expected a queued overlay mutation to be rejected after the ordered command boundary")
	}
	if output.Len() != 0 {
		t.Fatalf("expected no overlay output during foreground execution, got %q", output.Bytes())
	}
}

func TestBottomCompositorParsesMarkersAtEveryPTYReadBoundary(t *testing.T) {
	const marker = "test-session"
	transaction := append(terminalMarkerBytes(marker, "prompt-start"), []byte("λ ")...)
	transaction = append(transaction, terminalMarkerBytes(marker, "prompt-end")...)

	assertPrompt := func(t *testing.T, chunks ...[]byte) {
		t.Helper()
		var output bytes.Buffer
		compositor := newTerminalCompositor(&output, "bottom", marker, 20, 5)
		t.Cleanup(compositor.Close)
		for _, chunk := range chunks {
			compositor.WritePTY(chunk)
		}

		if bytes.Contains(output.Bytes(), []byte("777;vuja")) {
			t.Fatalf("expected internal markers to be consumed, got %q", output.Bytes())
		}
		screen := vt.NewEmulator(20, 5)
		t.Cleanup(func() { _ = screen.Close() })
		_, _ = screen.Write(output.Bytes())
		if got := screen.CursorPosition().Y; got != 4 {
			t.Fatalf("expected split prompt marker to anchor the cursor, got row %d", got)
		}
	}

	for split := 1; split < len(transaction); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			assertPrompt(t, transaction[:split], transaction[split:])
		})
	}

	byteChunks := make([][]byte, 0, len(transaction))
	for index := range transaction {
		byteChunks = append(byteChunks, transaction[index:index+1])
	}
	t.Run("one-byte-chunks", func(t *testing.T) {
		assertPrompt(t, byteChunks...)
	})
}

func TestBottomCompositorPreservesApplicationOSCAndRejectsOtherSessions(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 5)
	t.Cleanup(compositor.Close)

	title := []byte("\x1b]0;application title\a")
	otherSession := terminalMarkerBytes("another-session", "prompt-start")
	compositor.WritePTY(title)
	compositor.WritePTY(otherSession)

	if !bytes.Contains(output.Bytes(), title) {
		t.Fatal("expected application OSC sequence to pass through")
	}
	if !bytes.Contains(output.Bytes(), otherSession) {
		t.Fatal("expected another session's marker namespace to pass through unchanged")
	}
}

func TestBottomCompositorPreservesPromptTerminalSideEffects(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 20, 5)
	t.Cleanup(compositor.Close)

	title := []byte("\x1b]0;prompt title\a")
	workingDirectory := []byte("\x1b]7;file:///tmp/project\x1b\\")
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY(append(append(append([]byte(nil), title...), workingDirectory...), []byte("λ ")...))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	if count := bytes.Count(output.Bytes(), title); count != 1 {
		t.Fatalf("expected one prompt title update, got %d in %q", count, output.Bytes())
	}
	if count := bytes.Count(output.Bytes(), workingDirectory); count != 1 {
		t.Fatalf("expected one prompt working-directory update, got %d in %q", count, output.Bytes())
	}
	screen := applyTerminalOutput(t, output.Bytes(), 20, 5)
	if got := screenLine(screen, 4); !strings.Contains(got, "λ") {
		t.Fatalf("expected prompt content to remain pinned, got %q", got)
	}
}

func TestBottomCompositorLeavesClassicOutputUntouched(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "classic", "ignored", 20, 5)
	t.Cleanup(compositor.Close)

	input := []byte("prompt\noutput\x1b[31mred\x1b[0m")
	compositor.WritePTY(input)
	if !bytes.Equal(output.Bytes(), input) {
		t.Fatalf("expected classic mode passthrough, got %q", output.Bytes())
	}
	output.Reset()
	ui := []byte("\x1b7menu\x1b8")
	compositor.WriteUI(ui)
	if !bytes.Equal(output.Bytes(), ui) {
		t.Fatalf("expected classic UI passthrough, got %q", output.Bytes())
	}
}

func TestBottomCompositorFallsBackWithoutMarkerOrUsableGeometry(t *testing.T) {
	for _, test := range []struct {
		name   string
		marker string
		width  int
		height int
	}{
		{name: "missing marker", width: 20, height: 5},
		{name: "missing width", marker: "test", height: 5},
		{name: "missing height", marker: "test", width: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			compositor := newTerminalCompositor(&output, "bottom", test.marker, test.width, test.height)
			t.Cleanup(compositor.Close)

			input := []byte("unchanged output")
			compositor.WritePTY(input)
			if !bytes.Equal(output.Bytes(), input) {
				t.Fatalf("expected safe classic fallback, got %q", output.Bytes())
			}
		})
	}
}

func TestTerminalMarkerIDRequiresCryptographicRandomness(t *testing.T) {
	token := newTerminalMarkerIDFrom(strings.NewReader(strings.Repeat("x", 16)))
	if token != "78787878787878787878787878787878" {
		t.Fatalf("expected a 128-bit hex marker, got %q", token)
	}
	if token := newTerminalMarkerIDFrom(iotest.ErrReader(errors.New("unavailable"))); token != "" {
		t.Fatalf("expected bottom mode to remain disabled without random marker data, got %q", token)
	}
}

func TestBottomCompositorRestoresTerminalModesOnClose(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "bottom", "test-session", 20, 5)
	compositor.Close()

	for _, sequence := range []string{"\x1b[r", "\x1b[?7h", "\x1b[?25h"} {
		if !strings.Contains(output.String(), sequence) {
			t.Fatalf("expected cleanup to contain %q, got %q", sequence, output.String())
		}
	}
}

func TestBottomCompositorKeepsReloadedPromptAdjacentToPreviousExecution(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 32, 12)
	compositor.SetInputBoxTheme(testInputBoxTheme())

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ just install zsh"))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))
	compositor.WritePTY([]byte("installed\r\n"))
	compositor.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	compositor.Close()

	_, _ = output.WriteString("[VUJA] reloading...\r\n")
	reloaded := newTerminalCompositor(&output, "bottom", marker, 32, 12)
	reloaded.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(reloaded.Close)
	reloaded.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	reloaded.WritePTY([]byte("λ "))
	reloaded.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	lines := terminalScreenLines(applyTerminalOutput(t, output.Bytes(), 32, 12))
	installedRow := terminalLineIndex(lines, "installed", 0)
	reloadRow := terminalLineIndex(lines, "[VUJA] reloading...", installedRow+1)
	nextPromptRow := terminalLineIndex(lines, "λ", reloadRow+1)
	if installedRow < 0 || reloadRow < 0 || nextPromptRow < 0 {
		t.Fatalf("expected the previous execution, reload notice, and next prompt to remain visible, got %q", lines)
	}
	if strings.ContainsAny(strings.Join(lines, ""), "╭╮╰╯│├┤") {
		t.Fatalf("expected reload flow to remain borderless, got %q", lines)
	}
}

func TestBottomCompositorRestoresTerminalModesDuringPanicUnwind(t *testing.T) {
	var output bytes.Buffer
	func() {
		compositor := newTerminalCompositor(&output, "bottom", "test-session", 20, 5)
		defer compositor.Close()
		defer func() { _ = recover() }()
		panic("test panic")
	}()

	for _, sequence := range []string{"\x1b[r", "\x1b[?7h", "\x1b[?25h"} {
		if !strings.Contains(output.String(), sequence) {
			t.Fatalf("expected panic cleanup to contain %q, got %q", sequence, output.String())
		}
	}
}

func TestBottomCompositorBoundsIncompleteOSCBuffer(t *testing.T) {
	var output bytes.Buffer
	compositor := newTerminalCompositor(&output, "bottom", "test-session", 20, 5)
	t.Cleanup(compositor.Close)

	compositor.WritePTY(append([]byte("\x1b]0;"), bytes.Repeat([]byte("x"), 5000)...))
	if output.Len() == 0 {
		t.Fatal("expected oversized unterminated OSC data to be released instead of buffered indefinitely")
	}
}

func TestBottomCompositorSerializesConcurrentPTYAndUIWrites(t *testing.T) {
	var output lockedBuffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 40, 8)
	t.Cleanup(compositor.Close)
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("λ "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

	var wait sync.WaitGroup
	for index := range 20 {
		wait.Add(2)
		go func(value int) {
			defer wait.Done()
			compositor.WritePTY([]byte{byte('a' + value%26)})
		}(index)
		go func() {
			defer wait.Done()
			compositor.WriteUI([]byte("\x1b7\x1b8"))
		}()
	}
	wait.Wait()

	if bytes.Contains(output.Bytes(), []byte("777;vuja")) {
		t.Fatal("internal marker leaked during concurrent rendering")
	}
}

func BenchmarkTerminalCompositorInputRedraw(b *testing.B) {
	for _, benchmark := range []struct {
		name     string
		position string
		chatbox  bool
	}{
		{name: "classic", position: "classic"},
		{name: "bottom", position: "bottom"},
		{name: "bottom-chatbox", position: "bottom", chatbox: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			var output discardWriter
			const marker = "benchmark"
			compositor := newTerminalCompositor(output, benchmark.position, marker, 120, 40)
			if benchmark.chatbox {
				compositor.SetInputBoxTheme(testInputBoxTheme())
			}
			defer compositor.Close()
			compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
			compositor.WritePTY([]byte("λ "))
			compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				compositor.WritePTY([]byte("x"))
			}
		})
	}
}

func BenchmarkTerminalCompositorStreamedOutput(b *testing.B) {
	chunk := bytes.Repeat([]byte("streamed output 0123456789abcdef\r\n"), 128)
	for _, position := range []string{"classic", "bottom"} {
		b.Run(position, func(b *testing.B) {
			var output discardWriter
			const marker = "benchmark"
			compositor := newTerminalCompositor(output, position, marker, 120, 40)
			defer compositor.Close()
			compositor.WritePTY(terminalMarkerBytes(marker, "command-start"))

			b.ReportAllocs()
			b.SetBytes(int64(len(chunk)))
			b.ResetTimer()
			for range b.N {
				compositor.WritePTY(chunk)
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

type discardWriter struct{}

func (discardWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func testInputBoxTheme() terminalInputBoxTheme {
	return terminalInputBoxTheme{
		Background:                 "#080a0d",
		SurfaceBackground:          "#242528",
		CompletedSurfaceBackground: "#17191d",
		StatusBackground:           "#080a0d",
		StatusText:                 "#c6cad7",
		Border:                     "#739ee8",
		Accent:                     "#61ffcf",
		Muted:                      "#404658",
	}
}

func screenLine(screen *vt.Emulator, row int) string {
	var line strings.Builder
	for column := 0; column < screen.Width(); column++ {
		cell := screen.CellAt(column, row)
		if cell == nil || cell.Width == 0 {
			continue
		}
		line.WriteString(cell.Content)
	}
	return norm.NFD.String(strings.TrimRight(line.String(), " "))
}

func applyTerminalOutput(t *testing.T, output []byte, width, height int) *vt.Emulator {
	t.Helper()
	screen := vt.NewEmulator(width, height)
	t.Cleanup(func() { _ = screen.Close() })
	if _, err := screen.Write(output); err != nil {
		t.Fatalf("failed to apply terminal output: %v", err)
	}
	return screen
}

func terminalContainsLine(screen *vt.Emulator, expected string) bool {
	for row := 0; row < screen.Height(); row++ {
		if strings.Contains(screenLine(screen, row), expected) {
			return true
		}
	}
	return false
}

func terminalScreenLines(screen *vt.Emulator) []string {
	lines := make([]string, screen.Height())
	for row := range lines {
		lines[row] = screenLine(screen, row)
	}
	return lines
}

func terminalLineIndex(lines []string, expected string, start int) int {
	for index := max(start, 0); index < len(lines); index++ {
		if strings.Contains(lines[index], expected) {
			return index
		}
	}
	return -1
}

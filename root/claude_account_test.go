package root

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"golang.org/x/sys/unix"
)

func TestClaudeAccountAppearsAlongsideCodexByDefaultAndCanBeDisabled(t *testing.T) {
	for _, setting := range []string{"", "claude-account = false\n"} {
		cfg, err := config.Decode([]byte("[ui.chatbox]\n" + setting + "title-right = ['package']\n"))
		if err != nil {
			t.Fatal(err)
		}
		var snapshot statusSnapshot
		if err := json.Unmarshal([]byte(`{"Directory":"/fixture","ClaudeAccount":"claude@example.test","CodexAccount":"codex@example.test"}`), &snapshot); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		c := newTerminalCompositor(&output, "bottom", "claude-account", 140, 12)
		t.Cleanup(c.Close)
		c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
		c.SetStatusSnapshot(snapshot)
		line := ansi.Strip(c.inputBoxTitleLine())
		if !strings.Contains(line, "Codex codex@example.test") {
			t.Fatal("Claude setting changed the existing Codex element")
		}
		if setting == "" && !strings.HasSuffix(line, "Claude claude@example.test  ") {
			t.Fatalf("default Claude account missing from custom title's right edge: %q", line)
		}
		if setting != "" && strings.Contains(line, "claude@example.test") {
			t.Fatal("disabled Claude account rendered")
		}
	}
}

func claudeAccountFixture(t *testing.T, path, email string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"oauthAccount": map[string]string{"emailAddress": email}, "unrelated": "NEVER-DISPLAY"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClaudeAccountReadsSelectedAndLegacyMetadataWithoutMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	defaultPath := filepath.Join(home, ".claude.json")
	claudeAccountFixture(t, defaultPath, "default@example.test")
	if got := readClaudeAccount(claudeAccountPath()); got != "default@example.test" {
		t.Fatalf("default identity: %q", got)
	}
	selected := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", selected)
	if got := readClaudeAccount(claudeAccountPath()); got != "" {
		t.Fatal("selected profile fell back to default identity")
	}
	path := filepath.Join(selected, ".claude.json")
	want := claudeAccountFixture(t, path, "selected+tag@example.test")
	if got := readClaudeAccount(claudeAccountPath()); got != "selected+tag@example.test" {
		t.Fatalf("selected identity: %q", got)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("metadata was mutated")
	}
	legacy := filepath.Join(selected, ".config.json")
	claudeAccountFixture(t, legacy, "legacy@example.test")
	if got := readClaudeAccount(claudeAccountPath()); got != "legacy@example.test" {
		t.Fatalf("legacy precedence: %q", got)
	}
	if err := os.WriteFile(legacy, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(claudeAccountPath()); got != "" {
		t.Fatal("malformed legacy metadata fell through to another identity")
	}
}

func TestClaudeAccountMetadataBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	label := strings.Repeat("a", 256)
	data := claudeAccountFixture(t, path, label)
	data = append(data, bytes.Repeat([]byte(" "), codexAuthByteLimit-len(data))...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(path); got != label {
		t.Fatal("exact size or label boundary rejected")
	}
	if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("one-byte oversized metadata accepted")
	}
}

func TestClaudeAccountUnsafeOrUnavailableMetadataStaysHidden(t *testing.T) {
	for _, data := range []string{"{", `null`, `{}`, `{"oauthAccount":null}`, `{"oauthAccount":{"emailAddress":42}}`, `{"apiKey":"NEVER-DISPLAY"}`, `{"oauthAccount":{"emailAddress":"bad\u001b[2J"}}`, `{"oauthAccount":{"emailAddress":"bad\u202e"}}`, `{"oauthAccount":{"emailAddress":"a b"}}`} {
		path := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readClaudeAccount(path); got != "" {
			t.Fatalf("unsafe identity: %q", got)
		}
	}
	path := filepath.Join(t.TempDir(), ".claude.json")
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("missing metadata exposed identity")
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("FIFO metadata was read")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("directory metadata was read")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), codexAuthByteLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("oversized metadata was read")
	}
	claudeAccountFixture(t, path, strings.Repeat("x", 257))
	if got := readClaudeAccount(path); got != "" {
		t.Fatal("overlong identity rendered")
	}
}

func TestClaudeAccountRefreshClearsAndPreservesCodexWithoutMetrics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	path := filepath.Join(home, ".claude.json")
	claudeAccountFixture(t, path, "first@example.test")
	updates := make(chan statusSnapshot, 32)
	e := newStatusEngine(statusEngineOptions{ClaudeAccount: true, OnUpdate: func(s statusSnapshot) { updates <- s }})
	t.Cleanup(e.Close)
	e.snapshot.CodexAccount = "codex@example.test"
	e.SetCommandActive(true)
	e.StartMetrics(5 * time.Millisecond)
	wait := func(want string) statusSnapshot {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case s := <-updates:
				if s.ClaudeAccount == want {
					return s
				}
			case <-timer.C:
				t.Fatalf("Claude identity did not refresh to %q", want)
			}
		}
	}
	first := wait("first@example.test")
	claudeAccountFixture(t, path, "second@example.test")
	second := wait("second@example.test")
	if second.Revision <= first.Revision || second.CodexAccount != "codex@example.test" || second.HasCPU || second.HasMemory {
		t.Fatal("refresh changed metrics/Codex or failed to advance")
	}
	legacy := filepath.Join(home, ".claude", ".config.json")
	if err := os.Mkdir(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeAccountFixture(t, legacy, "legacy@example.test")
	_ = wait("legacy@example.test")
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	_ = wait("second@example.test")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cleared := wait(""); cleared.Revision <= second.Revision {
		t.Fatal("logout did not clear identity")
	}
	e.Close()
	select {
	case <-updates:
		t.Fatal("account published redundantly or after close")
	default:
	}
	disabled := newStatusEngine(statusEngineOptions{})
	defer disabled.Close()
	disabled.refreshClaudeAccount()
	if disabled.claudeAccountFile != "" || disabled.Snapshot().ClaudeAccount != "" {
		t.Fatal("disabled provider initialized metadata source")
	}
}

func TestClaudeAccountRoundtripAndPreviewUseOnlyFictionalIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claudeAccountFixture(t, filepath.Join(home, ".claude.json"), "NEVER-DISPLAY@example.test")
	for _, setting := range []string{"", "claude-account = false\n"} {
		cfg, err := config.Decode([]byte("[ui.chatbox]\n" + setting + "title-left = ['directory']\ntitle-right = ['package','versions']\n"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.UI.PromptPosition = "bottom"
		data, err := config.Render(cfg)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := config.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []string{"clean", "running", "history"} {
			preview, err := renderConfigScenario(reloaded, "fixture", 160, 24, "night", scenario)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(preview, "Codex person@example.test") || strings.Contains(preview, "NEVER-DISPLAY") {
				t.Fatal("preview used private identity or hid Codex")
			}
			if strings.Contains(preview, "Claude claude@example.test") != (setting == "") {
				t.Fatal("preview lost Claude toggle across roundtrip")
			}
		}
		if strings.Join(reloaded.UI.Chatbox.TitleRight, ",") != "package,versions" {
			t.Fatal("account toggle rewrote custom layout")
		}
		after, _ := config.Render(reloaded)
		if !bytes.Equal(data, after) {
			t.Fatal("preview mutated configuration")
		}
	}
}

func TestClaudeAccountFitsFrozenTitleAndRespectsNativeOwnership(t *testing.T) {
	c, out := frozenBadgeFixture(t)
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(config.DefaultConfig()))
	c.SetStatusSnapshot(statusSnapshot{Revision: 1, ClaudeAccount: "claude@example.test", CodexAccount: "codex@example.test"})
	c.Resize(140, 12)
	screen := applyTerminalOutput(t, out.Bytes(), 140, 12)
	line := screenLine(screen, c.viewportBadgeRow()-1)
	if !strings.Contains(line, "Back to bottom") || !strings.Contains(line, "Codex codex@example.test") || !strings.HasSuffix(line, "Claude claude@example.test") {
		t.Fatalf("frozen title: %q", line)
	}
	for _, width := range []int{20, 40, 120} {
		c.Resize(width, 12)
		if line := c.inputBoxTitleLine(); ansi.StringWidth(line) != width {
			t.Fatalf("title overflow: %d", ansi.StringWidth(line))
		}
	}
	c.Resize(140, 12)
	c.WritePTY(terminalMarkerBytes("badge", "command-start"))
	c.WritePTY([]byte("\x1b[?1049h"))
	out.Reset()
	c.SetStatusSnapshot(statusSnapshot{Revision: 2, ClaudeAccount: "changed@example.test"})
	if strings.Contains(out.String(), "changed@example.test") {
		t.Fatal("account drew over native application")
	}
	c.WritePTY([]byte("\x1b[?1049l"))
	c.WritePTY(terminalMarkerBytes("badge", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("badge", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("badge", "prompt-end"))
	returned := applyTerminalOutput(t, out.Bytes(), 140, 12)
	if !terminalContainsLine(returned, "Claude changed@example.test") {
		t.Fatal("native handoff lost refreshed Claude identity")
	}
	for _, height := range []int{4, 6} {
		c.height = height
		if c.inputBoxTitleEnabled() != (height == 6) {
			t.Fatal("Claude title ignored available height")
		}
	}
}

func TestClaudeAccountUpdatesTitleCacheAndHistoricalIdentity(t *testing.T) {
	e := newStatusEngine(statusEngineOptions{})
	defer e.Close()
	e.desired = "/fixture"
	e.snapshot.ClaudeAccount = "current@example.test"
	if !e.commitSnapshot("/fixture", statusSnapshot{Directory: "/fixture", ClaudeAccount: "stale@example.test"}) || e.Snapshot().ClaudeAccount != "current@example.test" {
		t.Fatal("directory refresh replaced identity")
	}
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "claude-update", 120, 12)
	defer c.Close()
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(config.DefaultConfig()))
	first := statusSnapshot{Revision: 1, ClaudeAccount: "first@example.test"}
	second := statusSnapshot{Revision: 2, ClaudeAccount: "second@example.test"}
	c.SetStatusSnapshot(first)
	_ = c.inputBoxTitleLine()
	c.SetStatusSnapshot(second)
	c.SetStatusSnapshot(first)
	if line := ansi.Strip(c.inputBoxTitleLine()); !strings.Contains(line, "Claude second@example.test") || strings.Contains(line, "first@example.test") {
		t.Fatalf("stale title: %q", line)
	}
	if statusSnapshotMetadataEqual(first, second) {
		t.Fatal("account change omitted from historical metadata")
	}
	c.commandStatusSnapshot = first
	if line := ansi.Strip(c.completedExecutionHeaderLine()); !strings.Contains(line, "Claude first@example.test") || strings.Contains(line, "second@example.test") {
		t.Fatalf("historical identity changed: %q", line)
	}
}

package root

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"golang.org/x/sys/unix"
)

func TestCodexAccountAppearsAtTitleRightAndCanBeDisabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := config.DefaultConfig()
		setting := "[ui.chatbox]\ncodex-account = true\n"
		if !enabled {
			setting = "[ui.chatbox]\ncodex-account = false\n"
		}
		if _, err := toml.Decode(setting, cfg); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		c := newTerminalCompositor(&output, "bottom", "account-test", 120, 12)
		t.Cleanup(c.Close)
		c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
		var snapshot statusSnapshot
		if err := json.Unmarshal([]byte(`{"Directory":"/tmp/project","CodexAccount":"person@example.test"}`), &snapshot); err != nil {
			t.Fatal(err)
		}
		c.SetStatusSnapshot(snapshot)
		line := ansi.Strip(c.inputBoxTitleLine())
		if enabled {
			if !strings.HasSuffix(line, "Codex person@example.test  ") {
				t.Fatalf("expected Codex account at right edge, got %q", line)
			}
		} else if strings.Contains(line, "example.test") {
			t.Fatalf("disabled account must not render, got %q", line)
		}
	}
}

func codexAccountFixture(t *testing.T, home, payload string) []byte {
	t.Helper()
	data := []byte(`{"tokens":{"id_token":"header.` + base64.RawURLEncoding.EncodeToString([]byte(payload)) + `.signature","access_token":"NEVER-DISPLAY-ACCESS","refresh_token":"NEVER-DISPLAY-REFRESH"}}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCodexAccountReadsOnlySelectedHomeAndIDTokenEmail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	defaultHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(defaultHome, 0o700); err != nil {
		t.Fatal(err)
	}
	codexAccountFixture(t, defaultHome, `{"email":"default@example.test","exp":1}`)
	if got := readCodexAccount(codexAccountHome()); got != "default@example.test" {
		t.Fatalf("unexpected default account %q", got)
	}
	selected := t.TempDir()
	wantBytes := codexAccountFixture(t, selected, `{"email":"selected+tag@example.test"}`)
	t.Setenv("CODEX_HOME", selected)
	if got := readCodexAccount(codexAccountHome()); got != "selected+tag@example.test" {
		t.Fatalf("unexpected selected account %q", got)
	}
	gotBytes, err := os.ReadFile(filepath.Join(selected, "auth.json"))
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatal("display reader changed credential file")
	}
}

func TestCodexAccountUnavailableAndUnsafeCredentialsStayHidden(t *testing.T) {
	for name, data := range map[string]string{
		"malformed-json": "{", "api-key": `{"OPENAI_API_KEY":"NEVER-DISPLAY-KEY"}`,
		"null-tokens": `{"tokens":null}`, "wrong-token-type": `{"tokens":{"id_token":42}}`,
		"malformed-jwt":     `{"tokens":{"id_token":"header.!.signature"}}`,
		"missing-signature": `{"tokens":{"id_token":"header.e30."}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := readCodexAccount(home); got != "" {
				t.Fatalf("unusable credentials exposed %q", got)
			}
		})
	}
	for _, payload := range []string{`{}`, `null`, `{"email":null}`, `{"email":42}`, `{"email":""}`, `{"email":"a\nb"}`, `{"email":"a\u001b]8;;url"}`, `{"email":"a\u202eb"}`, `{"email":"a b"}`, `{"email":"` + strings.Repeat("a", 257) + `"}`} {
		home := t.TempDir()
		codexAccountFixture(t, home, payload)
		if got := readCodexAccount(home); got != "" {
			t.Fatalf("unsafe or unavailable account exposed %q", got)
		}
	}
	home := t.TempDir()
	if got := readCodexAccount(home); got != "" {
		t.Fatalf("missing credentials exposed %q", got)
	}
	path := filepath.Join(home, "auth.json")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCodexAccount(home); got != "" {
		t.Fatal("non-regular credential file must be ignored without blocking")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), codexAuthByteLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCodexAccount(home); got != "" {
		t.Fatal("oversized credential file must be ignored")
	}
}

func TestCodexAccountPollsAndClearsWithoutMetricsOrDirectoryChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	codexAccountFixture(t, home, `{"email":"first@example.test"}`)
	updates := make(chan statusSnapshot, 32)
	engine := newStatusEngine(statusEngineOptions{CodexAccount: true, OnUpdate: func(s statusSnapshot) { updates <- s }})
	t.Cleanup(engine.Close)
	engine.SetCommandActive(true)
	engine.StartMetrics(5 * time.Millisecond)
	wait := func(want string) statusSnapshot {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case s := <-updates:
				if s.CodexAccount == want {
					return s
				}
			case <-timer.C:
				t.Fatalf("account did not update to %q", want)
			}
		}
	}
	first := wait("first@example.test")
	codexAccountFixture(t, home, `{"email":"other@example.test"}`)
	second := wait("other@example.test")
	if second.Revision <= first.Revision || second.HasCPU || second.HasMemory {
		t.Fatal("account must update independently of disabled metrics")
	}
	if err := os.Remove(filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	cleared := wait("")
	if cleared.Revision <= second.Revision {
		t.Fatal("credential removal did not advance revision")
	}
	engine.Close()
	select {
	case <-updates:
		t.Fatal("unexpected redundant account publication")
	default:
	}
	disabled := newStatusEngine(statusEngineOptions{})
	defer disabled.Close()
	disabled.refreshCodexAccount()
	if disabled.codexAccountHome != "" || disabled.Snapshot().CodexAccount != "" {
		t.Fatal("disabled provider must not initialize or read credential source")
	}
}

func TestCodexAccountSurvivesStatusRefreshAndInvalidatesTitleCache(t *testing.T) {
	engine := newStatusEngine(statusEngineOptions{})
	defer engine.Close()
	engine.desired = "/tmp/project"
	engine.snapshot.CodexAccount = "current@example.test"
	if !engine.commitSnapshot("/tmp/project", statusSnapshot{Directory: "/tmp/project", CodexAccount: "stale@example.test"}) || engine.Snapshot().CodexAccount != "current@example.test" {
		t.Fatal("directory/version refresh replaced current account")
	}
	var output bytes.Buffer
	c := newTerminalCompositor(&output, "bottom", "account-update", 80, 12)
	defer c.Close()
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(config.DefaultConfig()))
	first := statusSnapshot{Revision: 1, CodexAccount: "first@example.test"}
	second := statusSnapshot{Revision: 2, CodexAccount: "other@example.test"}
	c.SetStatusSnapshot(first)
	_ = c.inputBoxTitleLine()
	c.SetStatusSnapshot(second)
	c.SetStatusSnapshot(first)
	if got := ansi.Strip(c.inputBoxTitleLine()); !strings.Contains(got, "other@example.test") || strings.Contains(got, "first@example.test") {
		t.Fatalf("stale account or title cache: %q", got)
	}
	if statusSnapshotMetadataEqual(first, second) {
		t.Fatal("changed account must change historical metadata")
	}
}

func TestCodexAccountDefaultAndOffRoundtripPreserveCustomLayout(t *testing.T) {
	for _, setting := range []string{"", "codex-account = false\n"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[ui.chatbox]\n"+setting+"title-left = [\"git-branch\"]\ntitle-right = []\nstatus-left = []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := config.Render(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err = config.LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		c := newTerminalCompositor(&output, "bottom", "account-config", 100, 12)
		c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
		c.SetStatusSnapshot(statusSnapshot{CodexAccount: "person@example.test", Git: gitStatusSnapshot{Branch: "feature"}})
		line := ansi.Strip(c.inputBoxTitleLine())
		c.Close()
		if !strings.HasPrefix(line, "  feature") || strings.Contains(line, "example.test") != (setting == "") {
			t.Fatalf("roundtrip changed custom layout or account setting: %q", line)
		}
	}
}

func TestCodexAccountTitleRespectsOwnershipAndRestoresUpdatedIdentity(t *testing.T) {
	var output bytes.Buffer
	const marker = "account-ownership"
	c := newTerminalCompositor(&output, "bottom", marker, 100, 14)
	defer c.Close()
	c.SetInputBoxTheme(testInputBoxTheme())
	cfg := config.DefaultConfig()
	cfg.UI.Chatbox.OutputViewport = "pinned"
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	c.SetStatusSnapshot(statusSnapshot{Revision: 1, CodexAccount: "first@example.test"})
	c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	c.WritePTY([]byte("λ app"))
	c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	prompt := applyTerminalOutput(t, output.Bytes(), 100, 14)
	if !terminalContainsLine(prompt, "Codex first@example.test") {
		t.Fatal("account missing from actual managed prompt")
	}
	c.WritePTY(terminalMarkerBytes(marker, "command-start"))
	c.WritePTY([]byte("\x1b[?1049h\x1b[Hnative application"))
	active := applyTerminalOutput(t, output.Bytes(), 100, 14)
	if !terminalContainsLine(active, "native application") || terminalContainsLine(active, "Codex first@example.test") {
		t.Fatal("account chrome intruded on alternate-screen application")
	}
	c.SetStatusSnapshot(statusSnapshot{Revision: 2, CodexAccount: "other@example.test"})
	c.WritePTY([]byte("\x1b[?1049l"))
	c.WritePTY(terminalMarkerBytes(marker, "command-end:0"))
	c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	c.WritePTY([]byte("λ "))
	c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	returned := applyTerminalOutput(t, output.Bytes(), 100, 14)
	if !terminalContainsLine(returned, "Codex other@example.test") {
		t.Fatal("updated account missing on return to managed prompt")
	}
	c.commandStatusSnapshot = statusSnapshot{CodexAccount: "first@example.test"}
	if got := ansi.Strip(c.completedExecutionHeaderLine()); !strings.Contains(got, "first@example.test") || strings.Contains(got, "other@example.test") {
		t.Fatalf("snapshot must retain display identity at command start: %q", got)
	}
	for _, height := range []int{4, 6} {
		c.height = height
		if c.inputBoxTitleEnabled() != (height == 6) {
			t.Fatalf("title must respect available height %d", height)
		}
	}
}

func TestCodexAccountAcceptsPaddedPayloadAndRejectsAPIKeyMode(t *testing.T) {
	home := t.TempDir()
	token := "header." + base64.URLEncoding.EncodeToString([]byte(`{"email":"padded@example.test"}`)) + ".signature"
	for _, mode := range []string{"chatgpt", "api_key"} {
		data := []byte(`{"auth_mode":"` + mode + `","tokens":{"id_token":"` + token + `"}}`)
		if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		want := "padded@example.test"
		if mode == "api_key" {
			want = ""
		}
		if got := readCodexAccount(home); got != want {
			t.Fatalf("unexpected display value for %s: %q", mode, got)
		}
	}
}

func TestCodexAccountStaysAtTitleRightWhileBrowsingOlderOutput(t *testing.T) {
	c, output := frozenBadgeFixture(t)
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(config.DefaultConfig()))
	c.SetStatusSnapshot(statusSnapshot{
		CodexAccount: "person@example.test",
		Package:      projectPackageSnapshot{Name: "example-project", Version: "1.2.3"},
		Versions:     map[string]string{"go": "1.26.0"},
	})
	c.Resize(80, 12)
	screen := applyTerminalOutput(t, output.Bytes(), 80, 12)
	row := c.viewportBadgeRow() - 1
	line := screenLine(screen, row)
	if !strings.Contains(line, "Back to bottom") || !strings.HasSuffix(line, "Codex person@example.test") {
		t.Fatalf("frozen title must keep the account rightmost beside badge: %q", line)
	}
	if cell := screen.CellAt(77, row); cell == nil || cell.Content != "t" {
		t.Fatal("account must end immediately before the two padding columns")
	}
}

func TestCodexAccountTitleFitsPresetsAndNarrowWidths(t *testing.T) {
	for _, preset := range config.PresetNames {
		cfg, err := config.Preset(preset)
		if err != nil {
			t.Fatal(err)
		}
		for _, width := range []int{20, 40, 120} {
			var output bytes.Buffer
			c := newTerminalCompositor(&output, "bottom", "account-width", width, 12)
			c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
			c.SetStatusSnapshot(statusSnapshot{Directory: "/tmp/project", CodexAccount: "person@example.test"})
			line := c.inputBoxTitleLine()
			c.Close()
			if ansi.StringWidth(line) != width || !strings.Contains(ansi.Strip(line), "Codex") {
				t.Fatalf("account missing or title overflow for %s/%d: %q", preset, width, line)
			}
		}
		preview := renderConfigPreview(cfg, preset, 120, "night")
		if !strings.Contains(preview, "Codex person@example.test") || strings.Contains(preview, "NEVER-DISPLAY") {
			t.Fatal("preview must use fictional account")
		}
	}
}

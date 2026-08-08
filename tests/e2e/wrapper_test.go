package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestWrapperCompletesLiveDirectoryWithoutLeakingTerminalResponse(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is unavailable", shell)
			}
			testWrapperSession(t, shell)
		})
	}
}

func TestSetupPreventsDuplicateZshInitialization(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is unavailable")
	}

	repoRoot := repositoryRoot(t)
	tempDir := t.TempDir()
	homeDir := filepath.Join(tempDir, "home")
	binDir := filepath.Join(tempDir, "bin")
	configDir := filepath.Join(tempDir, "config")
	dataDir := filepath.Join(tempDir, "data")
	cacheDir := filepath.Join(tempDir, "cache")
	for _, dir := range []string{homeDir, binDir, configDir, dataDir, cacheDir, filepath.Join(configDir, "vuja")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	counterFile := filepath.Join(tempDir, "zsh-starts")
	rcContent := "print started >> " + shellQuote(counterFile) + "\n" +
		"PROMPT='vuja-startup> '\n" +
		"RPROMPT=\n\n" +
		"# Vuja Autocomplete\n" +
		`eval "$(vuja init zsh)"` + "\n"
	rcFile := filepath.Join(homeDir, ".zshrc")
	if err := os.WriteFile(rcFile, []byte(rcContent), 0644); err != nil {
		t.Fatal(err)
	}
	config := `[updater]
check-on-startup = false
channel = "stable"
check-interval = "24h"
`
	if err := os.WriteFile(filepath.Join(configDir, "vuja", "config.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(binDir, "vuja")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/vuja")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vuja: %v\n%s", err, output)
	}

	env := append(cleanEnvironment(),
		"HOME="+homeDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TERM=xterm-256color",
		"ZDOTDIR="+homeDir,
		"XDG_CACHE_HOME="+cacheDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_DATA_HOME="+dataDir,
	)
	setup := exec.CommandContext(t.Context(), binary, "setup", "zsh")
	setup.Env = env
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("configure zsh: %v\n%s", err, output)
	}

	sessionContext, cancelSession := context.WithCancel(t.Context())
	command := exec.CommandContext(sessionContext, "zsh", "-i")
	command.Env = env
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelSession()
		_ = terminal.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})

	var output synchronizedBuffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = output.ReadFrom(terminal)
		close(copyDone)
	}()
	waitForOutput(t, &output, "vuja-startup> ", 5*time.Second)

	starts, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(starts), "started"); count != 1 {
		t.Fatalf("expected shell initialization once, got %d:\n%s", count, starts)
	}

	_, _ = terminal.Write([]byte("exit\r"))
	select {
	case <-copyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapped zsh did not exit")
	}
}

func testWrapperSession(t *testing.T, shell string) {
	repoRoot := repositoryRoot(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	configDir := filepath.Join(tempDir, "config")
	dataDir := filepath.Join(tempDir, "data")
	cacheDir := filepath.Join(tempDir, "cache")
	homeDir := filepath.Join(tempDir, "home")
	workDir := filepath.Join(tempDir, "work")
	for _, dir := range []string{binDir, configDir, dataDir, cacheDir, homeDir, workDir, filepath.Join(configDir, "vuja")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	targetDir := filepath.Join(workDir, "scalar-docs")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	rcFile := ".bashrc"
	rcContent := "PS1='vuja-e2e> '\nPROMPT_COMMAND=\n"
	if shell == "zsh" {
		rcFile = ".zshrc"
		rcContent = "PROMPT='vuja-e2e> '\nRPROMPT=\n"
	}
	if err := os.WriteFile(filepath.Join(homeDir, rcFile), []byte(rcContent), 0644); err != nil {
		t.Fatal(err)
	}
	config := `[core]
mode = "spec"

[ui]
ghost-text = true
max-suggestions = 20
max-height = 6

[updater]
check-on-startup = false
channel = "stable"
check-interval = "24h"
`
	if err := os.WriteFile(filepath.Join(configDir, "vuja", "config.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(binDir, "vuja")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/vuja")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vuja: %v\n%s", err, output)
	}

	sessionContext, cancelSession := context.WithCancel(t.Context())
	command := exec.CommandContext(sessionContext, binary, "--shell", shell)
	command.Dir = workDir
	command.Env = append(os.Environ(),
		"HOME="+homeDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TERM=xterm-256color",
		"ZDOTDIR="+homeDir,
		"XDG_CACHE_HOME="+cacheDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_DATA_HOME="+dataDir,
	)
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelSession()
		_ = terminal.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})

	var output synchronizedBuffer
	copyDone := make(chan struct{})
	go func() {
		var backgroundReply sync.Once
		var cursorReply sync.Once
		readBuffer := make([]byte, 4096)
		for {
			n, readErr := terminal.Read(readBuffer)
			if n > 0 {
				_, _ = output.Write(readBuffer[:n])
				currentOutput := output.String()
				if strings.Contains(currentOutput, "\x1b]11;?\x1b\\") {
					backgroundReply.Do(func() {
						_, _ = terminal.Write([]byte("\x1b]11;rgb:0808/0a0a/0d0d\x1b\\"))
					})
				}
				if strings.Contains(currentOutput, "\x1b[6n") {
					cursorReply.Do(func() {
						_, _ = terminal.Write([]byte("\x1b[1;1R"))
					})
				}
			}
			if readErr != nil {
				break
			}
		}
		close(copyDone)
	}()

	waitForOutput(t, &output, "vuja-e2e> ", 5*time.Second)
	if strings.Contains(output.String(), "\x1b]11;?") {
		t.Fatal("vuja queried terminal background color during startup")
	}

	if _, err := terminal.Write([]byte("cd scalar-")); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &output, "scalar-docs/", 5*time.Second)
	beforeTerminalResponse := output.Len()
	if _, err := terminal.Write([]byte("\x1b]11;rgb:0808/0a0a/0d0d\x1b\\")); err != nil {
		t.Fatal(err)
	}
	beforeChangeDirectory := output.Len()
	if _, err := terminal.Write([]byte("\x1b[C\r")); err != nil {
		t.Fatal(err)
	}
	waitForOutputAfter(t, &output, beforeChangeDirectory, "vuja-e2e> ", 5*time.Second)
	beforePwd := output.Len()
	if _, err := terminal.Write([]byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	waitForOutputAfter(t, &output, beforePwd, targetDir, 5*time.Second)
	waitForOutputAfter(t, &output, beforePwd, "vuja-e2e> ", 5*time.Second)
	responseOutput := output.String()[beforeTerminalResponse:]
	if strings.Contains(responseOutput, "rgb:0808") {
		t.Fatalf("terminal color response leaked into shell input:\n%q", responseOutput)
	}

	_, _ = terminal.Write([]byte("exit\r"))
	select {
	case <-copyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("vuja did not exit after the child shell exited")
	}
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) ReadFrom(reader *os.File) (int64, error) {
	data := make([]byte, 4096)
	var total int64
	for {
		count, err := reader.Read(data)
		if count > 0 {
			written, writeErr := b.Write(data[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
		}
		if err != nil {
			return total, err
		}
	}
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *synchronizedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func waitForOutput(t *testing.T, output *synchronizedBuffer, expected string, timeout time.Duration) {
	t.Helper()
	waitForOutputAfter(t, output, 0, expected, timeout)
}

func waitForOutputAfter(t *testing.T, output *synchronizedBuffer, offset int, expected string, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		currentOutput := output.String()
		if offset <= len(currentOutput) && strings.Contains(currentOutput[offset:], expected) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %q in terminal output:\n%q", expected, output.String())
		case <-ticker.C:
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve repository root %s: %v", root, err)
	}
	return root
}

func cleanEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "VUJA_ACTIVE_SHELL", "VUJA_FD", "VUJA_IS_CHILD", "VUJA_PID":
			continue
		default:
			environment = append(environment, entry)
		}
	}
	return environment
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

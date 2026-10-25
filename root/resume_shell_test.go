package root

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

// This runs only a private Zsh and the generated hook, never a Vuja/Codex
// executable or user configuration. The extra FD is the editor action pipe.
type resumeShellFixture struct {
	terminal      *os.File
	requests      *os.File
	events        <-chan string
	ctx           context.Context
	output        *resumeShellOutput
	editor        *shellResumeEditor
	release       *os.File
	dropSubmitted *atomic.Bool
}

type resumeShellOutput struct {
	sync.Mutex
	text string
}

func (o *resumeShellOutput) Write(data []byte) (int, error) {
	o.Lock()
	defer o.Unlock()
	o.text += string(data)
	if len(o.text) > 12000 {
		o.text = o.text[len(o.text)-12000:]
	}
	return len(data), nil
}
func (o *resumeShellOutput) String() string { o.Lock(); defer o.Unlock(); return o.text }

func newResumeShellFixture(t *testing.T, options ...string) *resumeShellFixture {
	t.Helper()
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook.zsh")
	if err := os.WriteFile(hook, []byte(shellInitScript("zsh", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	ipcReader, ipcWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	requestReader, requestWriter, err := os.Pipe()
	if err != nil {
		_ = ipcReader.Close()
		_ = ipcWriter.Close()
		t.Fatal(err)
	}
	releaseReader, releaseWriter, err := os.Pipe()
	if err != nil {
		_ = ipcReader.Close()
		_ = ipcWriter.Close()
		_ = requestReader.Close()
		_ = requestWriter.Close()
		t.Fatal(err)
	}
	ackReader, ackWriter, err := os.Pipe()
	if err != nil {
		_ = ipcReader.Close()
		_ = ipcWriter.Close()
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = releaseReader.Close()
		_ = releaseWriter.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	command := exec.CommandContext(ctx, path, "-d", "-f", "-i")
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "ZDOTDIR=" + dir, "TMPDIR=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_DATA_HOME=" + dir, "XDG_CACHE_HOME=" + dir, "XDG_RUNTIME_DIR=" + dir, "TERM=xterm-256color", "VUJA_PID=1", "VUJA_IS_CHILD=true", "VUJA_FD=3", "VUJA_RESUME_FD=4", "VUJA_HISTORY_ACK_FD=6"}
	command.ExtraFiles = []*os.File{ipcWriter, requestReader, releaseReader, ackReader}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 12, Cols: 80})
	if err != nil {
		cancel()
		_ = ipcReader.Close()
		_ = ipcWriter.Close()
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = releaseReader.Close()
		_ = releaseWriter.Close()
		_ = ackReader.Close()
		_ = ackWriter.Close()
		t.Fatal(err)
	}
	_ = ipcWriter.Close()
	_ = requestReader.Close()
	_ = releaseReader.Close()
	_ = ackReader.Close()
	events := make(chan string, 128)
	editor := &shellResumeEditor{shell: "zsh", requests: requestWriter, wake: terminal}
	dropSubmitted := new(atomic.Bool)
	ipcDone, outputDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ipcDone)
		reader := bufio.NewReader(ipcReader)
		for {
			message, _, err := readShellMessage(reader, shellMessageByteLimit)
			if err != nil {
				return
			}
			text := string(message)
			if start, _ := parseCommandStartMessage(text); start {
				// Exercise the independent history handshake without a database
				// or any inherited live-session acknowledgement descriptor.
				_, _ = ackWriter.Write([]byte{'1'})
			}
			if !dropSubmitted.Load() || !strings.HasSuffix(text, ":submitted") {
				editor.Handle(text)
			}
			if strings.HasPrefix(text, "FIXTURE_") || strings.HasPrefix(text, "VUJA_RESUME_") {
				select {
				case events <- text:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	output := new(resumeShellOutput)
	go func() { defer close(outputDone); _, _ = io.Copy(output, terminal) }()
	t.Cleanup(func() {
		cancel()
		_ = terminal.Close()
		_ = requestWriter.Close()
		_ = releaseWriter.Close()
		_ = ackWriter.Close()
		_ = ipcReader.Close()
		_ = command.Process.Kill() // Only the captured fixture-owned Zsh.
		_ = command.Wait()
		<-ipcDone
		<-outputDone
	})
	f := &resumeShellFixture{terminal: terminal, requests: requestWriter, events: events, ctx: ctx, output: output, editor: editor, release: releaseWriter, dropSubmitted: dropSubmitted}
	setup := "source " + shellQuote(hook) + `
codex() { print -u 3 -N -r -- "FIXTURE_RUN:$#:$1:$2"; }
fixture_block() { print -u 3 -N -r -- FIXTURE_BLOCKED; read -r -u 5 _release; }
` + strings.Join(options, "\n") + `
_fixture_snapshot() { print -u 3 -N -r -- "FIXTURE_BUFFER:$BUFFER" "FIXTURE_CURSOR:$CURSOR" "FIXTURE_PREBUFFER:$PREBUFFER"; }
zle -N _fixture_snapshot
bindkey '^X^T' _fixture_snapshot
_fixture_editor_ready() { print -u 3 -N -r -- FIXTURE_EDITOR_READY; }
add-zle-hook-widget line-init _fixture_editor_ready
print -u 3 -N -r -- FIXTURE_READY
`
	// Apply keymap overrides within one command so rebinding ordinary Enter
	// cannot prevent the remaining fixture definitions from being evaluated.
	if _, err := terminal.Write([]byte("eval " + shellQuote(setup) + "\n")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_READY")
	f.wait(t, "FIXTURE_EDITOR_READY")
	return f
}

func (f *resumeShellFixture) wait(t *testing.T, want string) string {
	t.Helper()
	timer := time.NewTimer(1500 * time.Millisecond)
	defer timer.Stop()
	var seen []string
	for {
		select {
		case event := <-f.events:
			seen = append(seen, event)
			if event == want {
				return event
			}
			if strings.HasPrefix(event, "FIXTURE_RUN:") {
				t.Fatalf("unexpected shell execution: %q", event)
			}
		case <-timer.C:
			output := f.output.String()
			if len(output) > 1000 {
				output = output[len(output)-1000:]
			}
			t.Fatalf("shell editor did not report %q; received %q; screen tail %q", want, seen, output)
		case <-f.ctx.Done():
			t.Fatal("fixture shell timed out")
		}
	}
}

func (f *resumeShellFixture) snapshot(t *testing.T, buffer string) {
	t.Helper()
	if _, err := f.terminal.Write([]byte("\x18\x14")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_BUFFER:"+buffer)
}

func (f *resumeShellFixture) probe(t *testing.T, nonce string, deadline time.Time) {
	t.Helper()
	if _, err := fmt.Fprintf(f.requests, "probe\t%s\t%d.%09d\t%s\n", nonce, deadline.Unix(), deadline.Nanosecond(), testCodexResumeID); err != nil {
		t.Fatal(err)
	}
}

func TestCodexResumeShellOwnsWholeEditorAdmission(t *testing.T) {
	f := newResumeShellFixture(t)
	const suffix = "; printf extra"
	if _, err := f.terminal.Write([]byte(suffix + "\x01")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, suffix)
	f.wait(t, "FIXTURE_CURSOR:0")
	nonce := strings.Repeat("a", 32)
	f.probe(t, nonce, time.Now().Add(3*time.Second))
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":nonempty")
	f.snapshot(t, suffix)
	f.wait(t, "FIXTURE_CURSOR:0")
	// Clearing an edited prompt must allow retry, not require a new prompt.
	if _, err := f.terminal.Write([]byte("\x05\x15")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "")
	f.probe(t, nonce, time.Now().Add(3*time.Second))
	f.wait(t, "VUJA_RESUME_READY:"+nonce)
	if _, err := fmt.Fprintf(f.requests, "commit\t%s\n", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":armed")
	if _, err := fmt.Fprintf(f.terminal, "\x1b[777;%s~", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":submitted")
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
}

func TestCodexResumeShellRejectsContinuationAndExpiredRequests(t *testing.T) {
	f := newResumeShellFixture(t)
	if _, err := f.terminal.Write([]byte("printf 'unfinished\n")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "")
	f.wait(t, "FIXTURE_PREBUFFER:printf 'unfinished\n")
	nonce := strings.Repeat("b", 32)
	f.probe(t, nonce, time.Now().Add(3*time.Second))
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":nonempty")
	if _, err := f.terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "")
	f.probe(t, nonce, time.Now().Add(-time.Second))
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":expired")
	f.snapshot(t, "")
}

func TestCodexResumeShellSocketChecksActualEditorNotParentPrefix(t *testing.T) {
	f := newResumeShellFixture(t)
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	const suffix = "; printf must-not-run"
	if _, err := f.terminal.Write([]byte(suffix + "\x01")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, suffix)
	// The parent still says empty: only the real editor may admit this.
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, f.editor); err == nil {
		t.Fatal("hidden actual editor text was accepted")
	}
	f.snapshot(t, suffix)
	f.wait(t, "FIXTURE_CURSOR:0")
	if _, err := f.terminal.Write([]byte("\x05\x15")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "")
	// A stale parent-side nonempty query must not deny an actually empty editor.
	if err := dispatchResumeFixture(t, server, target, dir, false, "stale-prefix", 3, f.editor); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "")
}

func TestCodexResumeShellQueuedDuringCommandCannotExecuteLater(t *testing.T) {
	f := newResumeShellFixture(t)
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	if _, err := f.terminal.Write([]byte("fixture_block\r")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_BLOCKED")
	// Deliberately stale parent busy state. ZLE is not currently active.
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, f.editor); err == nil {
		t.Fatal("inactive editor falsely confirmed acceptance")
	}
	if _, err := io.WriteString(f.release, "release\n"); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "") // Also rejects any unexpected execution event.
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, f.editor); err != nil {
		t.Fatal("explicit retry after queued expiry:", err)
	}
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
}

func TestCodexResumeShellLostAcknowledgementCannotReplay(t *testing.T) {
	f := newResumeShellFixture(t)
	f.dropSubmitted.Store(true)
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, f.editor); err == nil {
		t.Fatal("lost acknowledgement falsely reported confirmed submission")
	}
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
	if err := dispatchCodexResumeURL(target, dir); err == nil {
		t.Fatal("uncertain executed action was re-armed")
	}
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "")
}

func TestCodexResumeShellViEditorAcceptsWithoutOrdinaryEnterBinding(t *testing.T) {
	f := newResumeShellFixture(t, "bindkey -v", "bindkey '^M' undefined-key")
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, f.editor); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
}

func TestCodexResumeUnverifiedEditorNeverFallsBackToInputInjection(t *testing.T) {
	for _, name := range []string{"bash", "fish", "unknown"} {
		t.Run(name, func(t *testing.T) {
			editor := &shellResumeEditor{shell: name}
			action := &codexResumeAction{id: testCodexResumeID, deadline: time.Now().Add(time.Second), result: make(chan codexResumeResult, 1)}
			reserved := false
			result := submitCodexResumeAction(action, false, editor, func() func() { reserved = true; return nil })
			if result.err == nil || result.consumed || reserved {
				t.Fatal("unverified editor accepted or consumed activation")
			}
		})
	}
}

func TestCodexResumeShellPreservesWhitespaceAndMultilinePaste(t *testing.T) {
	for _, text := range []string{" ", "echo café 日本語\necho tail"} {
		t.Run(fmt.Sprintf("bytes-%d", len(text)), func(t *testing.T) {
			f := newResumeShellFixture(t)
			if _, err := io.WriteString(f.terminal, "\x1b[200~"+text+"\x1b[201~"); err != nil {
				t.Fatal(err)
			}
			f.snapshot(t, text)
			server, dir := resumeFixtureServer(t)
			if err := dispatchResumeFixture(t, server, server.Observe(testCodexResumeID), dir, false, "", 0, f.editor); err == nil {
				t.Fatal("nonempty actual editor was accepted")
			}
			f.snapshot(t, text)
		})
	}
}

func TestCodexResumeShellDeadlineRecheckedAtActualAcceptance(t *testing.T) {
	f := newResumeShellFixture(t)
	nonce := strings.Repeat("c", 32)
	deadline := time.Now().Add(time.Second)
	f.probe(t, nonce, deadline)
	f.wait(t, "VUJA_RESUME_READY:"+nonce)
	if _, err := fmt.Fprintf(f.requests, "commit\t%s\n", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":armed")
	// Hold the one-shot editor action until its precise supplied deadline has
	// passed. No arbitrary sleep stands in for editor readiness or execution.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	if _, err := fmt.Fprintf(f.terminal, "\x1b[777;%s~", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":expired")
	f.snapshot(t, "")
}

func TestCodexResumeShellRechecksTextArrivingAfterPreparation(t *testing.T) {
	f := newResumeShellFixture(t)
	nonce := strings.Repeat("d", 32)
	f.probe(t, nonce, time.Now().Add(3*time.Second))
	f.wait(t, "VUJA_RESUME_READY:"+nonce)
	if _, err := fmt.Fprintf(f.requests, "commit\t%s\n", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":armed")
	const suffix = "; printf arrived-after-prepare"
	if _, err := io.WriteString(f.terminal, suffix+"\x01"); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, suffix)
	f.wait(t, "FIXTURE_CURSOR:0")
	if _, err := fmt.Fprintf(f.terminal, "\x1b[777;%s~", nonce); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "VUJA_RESUME_RESULT:"+nonce+":nonempty")
	f.snapshot(t, suffix)
	f.wait(t, "FIXTURE_CURSOR:0")
	if _, err := io.WriteString(f.terminal, "\x05\x15"); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "")
	server, dir := resumeFixtureServer(t)
	if err := dispatchResumeFixture(t, server, server.Observe(testCodexResumeID), dir, false, "", 0, f.editor); err != nil {
		t.Fatal("fresh explicit retry after actual rejection:", err)
	}
	f.wait(t, "FIXTURE_RUN:2:resume:"+testCodexResumeID)
}

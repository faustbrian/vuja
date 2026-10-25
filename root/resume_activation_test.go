package root

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type resumeWriteFunc func([]byte) (int, error)

func (f resumeWriteFunc) Write(data []byte) (int, error) { return f(data) }

// Action-server tests isolate transport/admission bookkeeping. Actual editor
// authority is exercised with the generated hook in resume_shell_test.go.
type resumeFixtureEditor struct {
	io.Writer
	buffer string
	offset int
}

func (e resumeFixtureEditor) Prepare(string, time.Time) error {
	if e.buffer != "" || e.offset != 0 {
		return errors.New("fixture editor is not empty")
	}
	return nil
}
func (resumeFixtureEditor) Cancel() {}

func dispatchResumeFixture(t *testing.T, server *codexResumeActionServer, target, dir string, busy bool, buffer string, offset int, writer io.Writer) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- dispatchCodexResumeURL(target, dir) }()
	select {
	case action := <-server.Actions():
		editor, ok := writer.(codexResumeEditor)
		if !ok {
			editor = resumeFixtureEditor{Writer: writer, buffer: buffer, offset: offset}
		}
		submitCodexResumeAction(action, busy, editor, nil)
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("resume dispatch did not reach its consumer")
	}
	select {
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("resume dispatch did not report its consumer result")
	}
	return nil
}

// Inspect emitted OSC 8 fields directly, independently of the VT model that
// generates them. A substring of the visible UUID alone is not an action.
func renderedResumeTarget(wire, id string) string {
	for {
		_, rest, found := strings.Cut(wire, "\x1b]8;")
		if !found {
			return ""
		}
		end := strings.IndexByte(rest, '\a')
		if st := strings.Index(rest, "\x1b\\"); st >= 0 && (end < 0 || st < end) {
			end = st
		}
		if end < 0 {
			return ""
		}
		_, target, _ := strings.Cut(rest[:end], ";")
		u, err := url.Parse(target)
		if err == nil && u.Scheme == "vuja" && u.Host == "codex-resume" && u.Path == "/"+id {
			return target
		}
		wire = rest[end+1:]
	}
}

func resumeFixtureServer(t *testing.T) (*codexResumeActionServer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "actions")
	server, err := newCodexResumeActionServerIn(dir, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server, dir
}

func TestCodexResumeNativeLiveLinkRetainsActivation(t *testing.T) {
	for _, width := range []int{60, 100} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			server, dir := resumeFixtureServer(t)
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "resume", width, 14)
			t.Cleanup(c.Close)
			c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100, OutputCheckpoints: "off"})
			c.SetCodexResumeLinker(server.Observe)
			c.WritePTY(terminalMarkerBytes("resume", "prompt-start"))
			c.WritePTY([]byte("› codex"))
			c.WritePTY(terminalMarkerBytes("resume", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("resume", "command-start"))
			c.WritePTY([]byte("\x1b[2;2Hprivate-application-canvas\r\n"))
			linkifier := newCodexResumeLinkifier(server.Observe)
			c.WritePTY(linkifier.Transform([]byte("To continue this session, run codex resume " + testCodexResumeID + "\r\n")))
			out.Reset()
			c.WritePTY(terminalMarkerBytes("resume", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("resume", "prompt-start"))
			c.WritePTY([]byte("› "))
			c.WritePTY(terminalMarkerBytes("resume", "prompt-end"))
			target := renderedResumeTarget(out.String(), testCodexResumeID)
			if target == "" {
				t.Fatal("resume ID is missing its live hyperlink")
			}
			var input bytes.Buffer
			if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err != nil {
				t.Fatalf("visible native-exit resume link cannot activate: %v", err)
			}
			if input.String() != "codex resume "+testCodexResumeID+"\r" {
				t.Fatal("activation submitted different input")
			}
			out.Reset()
			c.Resize(width-4, 16)
			repainted := renderedResumeTarget(out.String(), testCodexResumeID)
			if repainted != target {
				t.Fatal("resize changed or discarded the action target")
			}
			if err := dispatchCodexResumeURL(repainted, dir); err == nil {
				t.Fatal("repaint re-armed a consumed activation")
			}
			if strings.Contains(strings.Join(retainedModelRows(c.viewport.model), "\n"), "private-application-canvas") {
				t.Fatal("resume retention admitted private canvas text")
			}
		})
	}
}

func TestCodexResumeUnsafeClickCanRetry(t *testing.T) {
	for _, state := range []struct {
		name   string
		busy   bool
		buffer string
		offset int
	}{
		{"busy", true, "", 0}, {"typed", false, "git status", 0}, {"cursor", false, "", 1},
	} {
		t.Run(state.name, func(t *testing.T) {
			server, dir := resumeFixtureServer(t)
			target := server.Observe(testCodexResumeID)
			var input bytes.Buffer
			err := dispatchResumeFixture(t, server, target, dir, state.busy, state.buffer, state.offset, &input)
			if err == nil {
				t.Fatal("unsafe click falsely reports success")
			}
			if input.Len() != 0 {
				t.Fatal("unsafe click wrote terminal input")
			}
			// handle writes the socket reply before publishing the separate
			// notice; dispatch completion does not order notice delivery.
			select {
			case notice, open := <-server.Notices():
				if !open || notice == "" || !strings.Contains(err.Error(), notice) {
					t.Fatalf("rejection notice does not identify the failure: %q, %v", notice, err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("rejection has no terminal notice")
			}
			if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err != nil {
				t.Fatal("safe explicit retry rejected:", err)
			}
			if input.String() != "codex resume "+testCodexResumeID+"\r" {
				t.Fatal("retry did not submit exactly one command")
			}
			if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err == nil {
				t.Fatal("successful retry left the target executable")
			}
			if input.String() != "codex resume "+testCodexResumeID+"\r" {
				t.Fatal("consumed retry submitted duplicate input")
			}
		})
	}
}

func TestCodexResumeExpiredActivationReportsFailure(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	server.mu.Lock()
	server.observed[testCodexResumeID] = time.Now().Add(-time.Second)
	server.mu.Unlock()
	if err := dispatchCodexResumeURL(target, dir); err == nil {
		t.Fatal("expired resume link falsely reports success")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("token", "fedcba9876543210fedcba9876543210")
	parsed.RawQuery = query.Encode()
	if err := dispatchCodexResumeURL(parsed.String(), dir); err == nil {
		t.Fatal("rejected credentials falsely report success")
	}
	valid := server.Observe(testCodexResumeID)
	if err := dispatchCodexResumeURL(parsed.String(), dir); err == nil {
		t.Fatal("wrong token was accepted for a live observation")
	}
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, server, valid, dir, false, "", 0, &input); err != nil {
		t.Fatal("unrelated invalid activation poisoned a valid observation:", err)
	}
	select {
	case <-server.Actions():
		t.Fatal("invalid activation reached shell")
	default:
	}
}

func TestCodexResumeNativeCheckpointRebindsWithoutLeakingCapabilities(t *testing.T) {
	old, actions := resumeFixtureServer(t)
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "pane", "old", old.Observe)
	c.WritePTY(terminalMarkerBytes("old", "command-start"))
	c.WritePTY([]byte("\x1b[2;2Hprivate-checkpoint-canvas\r\n"))
	l := newCodexResumeLinkifier(old.Observe)
	c.WritePTY(l.Transform([]byte("To continue this session, run codex resume " + testCodexResumeID + "\r\n")))
	c.WritePTY(terminalMarkerBytes("old", "command-end:0"))
	c.Close()
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(old.token)) || bytes.Contains(data, []byte(old.socketPath)) || bytes.Contains(data, []byte("private-checkpoint-canvas")) {
		t.Fatal("native checkpoint leaked private canvas or activation credentials")
	}
	stale := old.Observe(testCodexResumeID)
	old.Close()
	fresh, err := newCodexResumeActionServerIn(actions, "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	_, out := recoveryCompositor(t, dir, "pane", "fresh", fresh.Observe)
	target := renderedResumeTarget(out.String(), testCodexResumeID)
	if target == "" || target == stale {
		t.Fatal("restored native summary did not rebind to the new owner")
	}
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, fresh, target, actions, false, "", 0, &input); err != nil {
		t.Fatal(err)
	}
	if input.String() != "codex resume "+testCodexResumeID+"\r" {
		t.Fatal("restored owner received the wrong command")
	}
	if err := dispatchCodexResumeURL(stale, actions); err == nil {
		t.Fatal("stale owner action was accepted")
	}
	_, plain := recoveryCompositor(t, dir, "pane", "plain", nil)
	if renderedResumeTarget(plain.String(), testCodexResumeID) != "" {
		t.Fatal("missing owner advertised a dead restored action")
	}
}

func TestCodexResumeNativeWithoutOwnerIsNotAdvertisedAsAnAction(t *testing.T) {
	c, out := recoveryCompositor(t, t.TempDir(), "pane", "plain", nil)
	c.WritePTY(terminalMarkerBytes("plain", "command-start"))
	c.WritePTY([]byte("\x1b[2;2Hprivate-canvas\r\nTo continue this session, run codex resume " + testCodexResumeID + "\r\n"))
	out.Reset()
	c.WritePTY(terminalMarkerBytes("plain", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("plain", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("plain", "prompt-end"))
	if renderedResumeTarget(out.String(), testCodexResumeID) != "" {
		t.Fatal("missing owner advertised a dead native action")
	}
	if !strings.Contains(strings.Join(retainedModelRows(c.viewport.model), "\n"), testCodexResumeID) {
		t.Fatal("missing owner discarded copyable resume metadata")
	}
}

func TestCodexResumeWriteFailureAndPartialDelivery(t *testing.T) {
	for _, n := range []int{0, 4} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			server, dir := resumeFixtureServer(t)
			target := server.Observe(testCodexResumeID)
			writer := resumeWriteFunc(func([]byte) (int, error) { return n, errors.New("fixture write failed") })
			if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, writer); err == nil {
				t.Fatal("failed write falsely reports success")
			}
			var input bytes.Buffer
			if n == 0 {
				if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err != nil {
					t.Fatal("zero-byte failure consumed retry:", err)
				}
			} else if err := dispatchCodexResumeURL(target, dir); err == nil {
				t.Fatal("ambiguous partial write was re-armed")
			}
		})
	}
}

func TestCodexResumeAmbiguousWriteReservesPromptAcrossIDs(t *testing.T) {
	for _, n := range []int{0, 4, len("codex resume " + testCodexResumeID + "\r")} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			server, dir := resumeFixtureServer(t)
			first := server.Observe(testCodexResumeID)
			const secondID = "01a0e5a4-0838-70f2-abbc-bfe8c5ae0583"
			second := server.Observe(secondID)
			busy := false
			var input bytes.Buffer
			result := make(chan error, 1)
			go func() { result <- dispatchCodexResumeURL(first, dir) }()
			action := <-server.Actions()
			submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: resumeWriteFunc(func(data []byte) (int, error) { input.Write(data[:n]); return n, errors.New("fixture write error") })}, func() func() {
				busy = true
				return func() { busy = false }
			})
			if err := <-result; err == nil {
				t.Fatal("write error falsely reported success")
			}
			before := input.Len()
			err := dispatchResumeFixture(t, server, second, dir, busy, "", 0, &input)
			if n == 0 {
				if busy || err != nil {
					t.Fatal("definite zero-byte failure did not release the prompt:", err)
				}
			} else {
				if !busy || err == nil || input.Len() != before {
					t.Fatal("ambiguous write released the prompt and admitted another ID")
				}
				// Only an authoritative prompt/cancel reconciliation permits a new click.
				busy = false
				if err := dispatchResumeFixture(t, server, second, dir, busy, "", 0, &input); err != nil {
					t.Fatal("explicit retry after reconciliation failed:", err)
				}
			}
		})
	}
}

func TestCodexResumeQueuedTimeoutCannotExecuteLater(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	if err := dispatchCodexResumeURL(target, dir); err == nil {
		t.Fatal("unresponsive consumer falsely reports success")
	}
	action := <-server.Actions()
	var input bytes.Buffer
	submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: &input}, nil)
	if input.Len() != 0 {
		t.Fatal("timed-out request executed later")
	}
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err != nil {
		t.Fatal("timed-out queued request consumed explicit retry:", err)
	}
}

func TestCodexResumeInFlightTimeoutIsAmbiguousAndNeverReplayed(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	result := make(chan error, 1)
	go func() { result <- dispatchCodexResumeURL(target, dir) }()
	action := <-server.Actions()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: resumeWriteFunc(func(data []byte) (int, error) { close(started); <-release; return len(data), nil })}, nil)
	}()
	defer func() { close(release); <-finished }()
	<-started
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("in-flight timeout falsely reports success")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("in-flight write left the caller hanging")
	}
	if err := dispatchCodexResumeURL(target, dir); err == nil {
		t.Fatal("uncertain in-flight write allowed duplicate activation")
	}
}

func TestCodexResumeExpiredQueuedTransportCannotExecute(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	connection, err := (&net.Dialer{}).DialContext(t.Context(), "unix", server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(connection, "%s\t%s\t%d\n", server.token, testCodexResumeID, time.Now().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || !strings.HasPrefix(reply, "error\t") {
		t.Fatal("expired transport did not report rejection")
	}
	select {
	case <-server.Actions():
		t.Fatal("expired transport reached shell")
	default:
	}
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, server, target, dir, false, "", 0, &input); err != nil {
		t.Fatal("expired request consumed a valid link:", err)
	}
}

func TestCodexResumeExpiredRequestAtConsumerDoesNotSubmit(t *testing.T) {
	// The deadline can pass before the server's timer goroutine is scheduled.
	// The consumer must enforce it too, immediately before any reservation/I/O.
	action := &codexResumeAction{id: testCodexResumeID, deadline: time.Now().Add(-time.Second), result: make(chan codexResumeResult, 1)}
	var input bytes.Buffer
	reserved := false
	result := submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: &input}, func() func() { reserved = true; return nil })
	if result.err == nil || reserved || input.Len() != 0 {
		t.Fatal("expired pending action reached the terminal consumer")
	}
}

func TestCodexResumeConcurrentClicksSubmitAtMostOnce(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	target := server.Observe(testCodexResumeID)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- dispatchCodexResumeURL(target, dir) }()
	}
	var input bytes.Buffer
	accepted := 0
	for completed := 0; completed < 2; {
		select {
		case action := <-server.Actions():
			submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: &input}, nil)
		case err := <-results:
			if err == nil {
				accepted++
			}
			completed++
		case <-time.After(4 * time.Second):
			t.Fatal("concurrent activation did not finish")
		}
	}
	if accepted != 1 || input.String() != "codex resume "+testCodexResumeID+"\r" {
		t.Fatal("concurrent clicks did not submit exactly one command")
	}
}

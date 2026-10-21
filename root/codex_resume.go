package root

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

const (
	codexResumeLinePrefix      = "To continue this session, run"
	codexReconnectLinePrefix   = "To reconnect, run"
	codexSessionIDLinePrefix   = "Session ID:"
	codexResumeLineLimit       = 2048
	codexResumeTTL             = 10 * time.Minute
	codexResumeSocketPathLimit = 100
	codexResumeActionTimeout   = time.Second
)

var codexResumeIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// Credential-free checkpoint placeholders are metadata, not working actions.
// Keep them in retained rows for rebinding, but never advertise them as links.
var bareCodexResumeLink = regexp.MustCompile(`\x1b]8;[^;\x07\x1b]*;vuja://codex-resume/[0-9a-fA-F-]{36}(?:\x07|\x1b\\)`)

func (c *terminalCompositor) SetCodexResumeLinker(linkFor func(string) string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumeLinkFor = linkFor
}

type codexResumeLinkifier struct {
	linkFor              func(string) string
	prefixMatch          int
	reconnectPrefixMatch int
	sessionPrefixMatch   int
	escapeState          codexResumeEscapeState
	captureEscapeState   codexResumeEscapeState
	captureEscapeStart   int
	capturing            bool
	awaitingResumeLine   bool
	line                 []byte
}

type codexResumeEscapeState uint8

const (
	codexResumeEscapeNone codexResumeEscapeState = iota
	codexResumeEscapeStart
	codexResumeEscapeCSI
	codexResumeEscapeOSC
	codexResumeEscapeOSCEscape
)

func newCodexResumeLinkifier(linkFor func(string) string) *codexResumeLinkifier {
	return &codexResumeLinkifier{linkFor: linkFor}
}

func (l *codexResumeLinkifier) Transform(data []byte) []byte {
	if l == nil || l.linkFor == nil || len(data) == 0 {
		return data
	}
	resumePrefix := []byte(codexResumeLinePrefix)
	reconnectPrefix := []byte(codexReconnectLinePrefix)
	sessionPrefix := []byte(codexSessionIDLinePrefix)
	var output bytes.Buffer
	for _, char := range data {
		if l.capturing {
			l.line = append(l.line, char)
			if handled, boundary := l.consumeCapturedEscape(char); handled {
				if boundary {
					output.Write(l.linkLine(l.line[:l.captureEscapeStart]))
					output.Write(l.line[l.captureEscapeStart:])
					l.resetCapture()
				}
				continue
			}
			if char == '\n' {
				output.Write(l.linkLine(l.line))
				if !l.awaitingResumeLine && isCodexResumeContinuation(l.line) {
					l.line = l.line[:0]
					l.captureEscapeState = codexResumeEscapeNone
					l.captureEscapeStart = -1
					l.awaitingResumeLine = true
				} else {
					l.resetCapture()
				}
			} else if len(l.line) > codexResumeLineLimit {
				output.Write(l.line)
				l.resetCapture()
			}
			continue
		}

		output.WriteByte(char)
		if l.consumeEscape(char) {
			continue
		}
		var matched bool
		l.prefixMatch, matched = advanceCodexLinePrefix(char, resumePrefix, l.prefixMatch)
		var reconnectMatched bool
		l.reconnectPrefixMatch, reconnectMatched = advanceCodexLinePrefix(char, reconnectPrefix, l.reconnectPrefixMatch)
		var sessionMatched bool
		l.sessionPrefixMatch, sessionMatched = advanceCodexLinePrefix(char, sessionPrefix, l.sessionPrefixMatch)
		if matched || reconnectMatched || sessionMatched {
			l.prefixMatch = 0
			l.reconnectPrefixMatch = 0
			l.sessionPrefixMatch = 0
			l.capturing = true
			l.captureEscapeStart = -1
		}
	}
	return output.Bytes()
}

func advanceCodexLinePrefix(char byte, prefix []byte, matched int) (int, bool) {
	if len(prefix) == 0 {
		return 0, false
	}
	if char == prefix[matched] {
		matched++
		if matched == len(prefix) {
			return 0, true
		}
		return matched, false
	}
	if char == prefix[0] {
		return 1, false
	}
	return 0, false
}

func (l *codexResumeLinkifier) consumeCapturedEscape(char byte) (bool, bool) {
	switch l.captureEscapeState {
	case codexResumeEscapeStart:
		switch char {
		case '[':
			l.captureEscapeState = codexResumeEscapeCSI
		case ']':
			l.captureEscapeState = codexResumeEscapeOSC
		default:
			l.captureEscapeState = codexResumeEscapeNone
			return true, true
		}
		return true, false
	case codexResumeEscapeCSI:
		if char < 0x40 || char > 0x7e {
			return true, false
		}
		l.captureEscapeState = codexResumeEscapeNone
		inline := char == 'm'
		if inline {
			l.captureEscapeStart = -1
		}
		return true, !inline
	case codexResumeEscapeOSC:
		switch char {
		case '\a':
			return true, !l.finishCapturedOSC(1)
		case '\x1b':
			l.captureEscapeState = codexResumeEscapeOSCEscape
		}
		return true, false
	case codexResumeEscapeOSCEscape:
		if char == '\\' {
			return true, !l.finishCapturedOSC(2)
		}
		l.captureEscapeState = codexResumeEscapeOSC
		return true, false
	case codexResumeEscapeNone:
		if char == '\x1b' {
			l.captureEscapeStart = len(l.line) - 1
			l.captureEscapeState = codexResumeEscapeStart
			return true, false
		}
	}
	return false, false
}

func (l *codexResumeLinkifier) finishCapturedOSC(terminatorLength int) bool {
	end := len(l.line) - terminatorLength
	payloadStart := l.captureEscapeStart + 2
	inline := payloadStart <= end && bytes.HasPrefix(l.line[payloadStart:end], []byte("8;"))
	l.captureEscapeState = codexResumeEscapeNone
	if inline {
		l.captureEscapeStart = -1
	}
	return inline
}

func (l *codexResumeLinkifier) resetCapture() {
	l.line = l.line[:0]
	l.capturing = false
	l.captureEscapeState = codexResumeEscapeNone
	l.captureEscapeStart = -1
	l.awaitingResumeLine = false
	l.prefixMatch = 0
	l.reconnectPrefixMatch = 0
	l.sessionPrefixMatch = 0
}

func isCodexResumeContinuation(line []byte) bool {
	return strings.TrimSpace(ansi.Strip(string(line))) == ":"
}

func (l *codexResumeLinkifier) consumeEscape(char byte) bool {
	switch l.escapeState {
	case codexResumeEscapeStart:
		switch char {
		case '[':
			l.escapeState = codexResumeEscapeCSI
		case ']':
			l.escapeState = codexResumeEscapeOSC
		default:
			l.escapeState = codexResumeEscapeNone
		}
		return true
	case codexResumeEscapeCSI:
		if char >= 0x40 && char <= 0x7e {
			l.escapeState = codexResumeEscapeNone
		}
		return true
	case codexResumeEscapeOSC:
		switch char {
		case '\a':
			l.escapeState = codexResumeEscapeNone
		case '\x1b':
			l.escapeState = codexResumeEscapeOSCEscape
		}
		return true
	case codexResumeEscapeOSCEscape:
		if char == '\\' {
			l.escapeState = codexResumeEscapeNone
		} else {
			l.escapeState = codexResumeEscapeOSC
		}
		return true
	case codexResumeEscapeNone:
		if char == '\x1b' {
			l.escapeState = codexResumeEscapeStart
			return true
		}
	}
	return false
}

func (l *codexResumeLinkifier) Flush() []byte {
	if l == nil || len(l.line) == 0 {
		return nil
	}
	pending := append([]byte(nil), l.line...)
	l.resetCapture()
	return pending
}

func (l *codexResumeLinkifier) linkLine(line []byte) []byte {
	matches := codexResumeIDPattern.FindAllIndex(line, -1)
	if len(matches) == 0 {
		return line
	}
	var output bytes.Buffer
	start := 0
	for _, match := range matches {
		id := string(line[match[0]:match[1]])
		actionURL := l.linkFor(id)
		if actionURL == "" {
			continue
		}
		output.Write(line[start:match[0]])
		fmt.Fprintf(&output, "\x1b]8;id=vuja-codex-%s;%s\x1b\\%s\x1b]8;;\x1b\\", id, actionURL, id)
		start = match[1]
	}
	if start == 0 {
		return line
	}
	output.Write(line[start:])
	return output.Bytes()
}

type codexResumeActionServer struct {
	listener   *net.UnixListener
	socketPath string
	token      string
	actions    chan *codexResumeAction
	notices    chan string
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	observed   map[string]time.Time
}

type codexResumeResult struct {
	consumed bool
	err      error
}

// Claiming and cancellation share a lock: a queued timeout cannot run later.
// Once a terminal write starts, a timeout is ambiguous and must not re-arm it.
type codexResumeAction struct {
	id       string
	deadline time.Time
	mu       sync.Mutex
	done     bool
	result   chan codexResumeResult
}

func (a *codexResumeAction) reject(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done {
		a.done = true
		a.result <- codexResumeResult{err: err}
	}
}

func (a *codexResumeAction) run(submit func() codexResumeResult) codexResumeResult {
	a.mu.Lock()
	if a.done {
		a.mu.Unlock()
		return codexResumeResult{err: errors.New("resume request is no longer active")}
	}
	if !a.deadline.After(time.Now()) {
		a.done = true
		result := codexResumeResult{err: errors.New("resume activation request expired; click again")}
		a.result <- result
		a.mu.Unlock()
		return result
	}
	a.done = true
	a.mu.Unlock()
	result := submit()
	a.result <- result
	return result
}

func (a *codexResumeAction) cancel(err error) codexResumeResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case result := <-a.result:
		return result
	default:
	}
	if !a.done {
		a.done = true
		return codexResumeResult{err: err}
	}
	return codexResumeResult{consumed: true, err: errors.New("resume submission status is unknown; inspect the terminal before retrying manually")}
}

func submitCodexResumeAction(a *codexResumeAction, shellBusy bool, writer io.Writer, reserve func() func()) codexResumeResult {
	if shellBusy {
		err := errors.New("shell is busy; click again at an idle empty prompt")
		a.reject(err)
		return codexResumeResult{err: err}
	}
	payload := codexResumeInput(a.id, false, "", 0)
	if len(payload) == 0 {
		err := errors.New("invalid Codex resume session ID")
		a.reject(err)
		return codexResumeResult{err: err}
	}
	editor, ok := writer.(codexResumeEditor)
	if !ok {
		err := errors.New("shell editor activation is unavailable; run the displayed command manually")
		a.reject(err)
		return codexResumeResult{err: err}
	}
	if err := editor.Prepare(a.id, a.deadline); err != nil {
		a.reject(err)
		return codexResumeResult{err: err}
	}
	defer editor.Cancel()
	return a.run(func() codexResumeResult {
		var rollback func()
		if reserve != nil {
			rollback = reserve()
		}
		n, err := writer.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			// Only a definite zero-byte failure releases the prompt reservation.
			// Positive-byte errors may already have submitted a command.
			if n == 0 && rollback != nil {
				rollback()
			}
			if n > 0 {
				return codexResumeResult{consumed: true, err: errors.New("resume input may have been submitted; inspect the terminal before retrying manually")}
			}
			return codexResumeResult{err: errors.New("resume input could not be submitted; click again when the terminal is available")}
		}
		return codexResumeResult{consumed: true}
	})
}

func newCodexResumeActionServer() (*codexResumeActionServer, error) {
	directory, err := codexResumeActionsDir()
	if err != nil {
		return nil, err
	}
	token := newTerminalMarkerID()
	if token == "" {
		return nil, errors.New("generate Codex resume action token")
	}
	return newCodexResumeActionServerIn(directory, token)
}

func newCodexResumeActionServerIn(directory, token string) (*codexResumeActionServer, error) {
	if !isCodexResumeToken(token) {
		return nil, errors.New("invalid Codex resume action token")
	}
	if err := config.EnsurePrivateDir(directory); err != nil {
		return nil, err
	}
	socketPath, err := codexResumeSocketPath(directory, token)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(socketPath)
	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("listen for Codex resume actions: %w", err)
	}
	if err := os.Chmod(socketPath, config.PrivateFileMode); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("restrict Codex resume action socket: %w", err)
	}
	server := &codexResumeActionServer{
		listener: listener, socketPath: socketPath, token: token,
		actions: make(chan *codexResumeAction, 4), notices: make(chan string, 4),
		stop: make(chan struct{}), done: make(chan struct{}), observed: make(map[string]time.Time),
	}
	go server.serve()
	return server, nil
}

func codexResumeSocketPath(actionsDir, token string) (string, error) {
	name := fmt.Sprintf("%d-%s.sock", os.Getpid(), token[:12])
	preferred := filepath.Join(actionsDir, name)
	if len(preferred) < codexResumeSocketPathLimit {
		return preferred, nil
	}
	runtimeDir, err := codexResumeRuntimeActionsDir(actionsDir)
	if err != nil {
		return "", fmt.Errorf("codex resume action socket path is too long: %s: %w", preferred, err)
	}
	if err := config.EnsurePrivateDir(runtimeDir); err != nil {
		return "", err
	}
	fallback := filepath.Join(runtimeDir, name)
	if len(fallback) >= codexResumeSocketPathLimit {
		return "", fmt.Errorf("codex resume action socket fallback is too long: %s", fallback)
	}
	return fallback, nil
}

func codexResumeRuntimeActionsDir(actionsDir string) (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" && runtime.GOOS == "darwin" {
		base = os.Getenv("TMPDIR")
	}
	if base == "" {
		return "", errors.New("no private runtime directory is available")
	}
	sum := sha256.Sum256([]byte(filepath.Clean(actionsDir)))
	return filepath.Join(base, "vja", hex.EncodeToString(sum[:4])), nil
}

func (s *codexResumeActionServer) Observe(id string) string {
	if s == nil || !isCodexResumeID(id) {
		return ""
	}
	now := time.Now()
	s.mu.Lock()
	for observedID, expires := range s.observed {
		if !expires.After(now) {
			delete(s.observed, observedID)
		}
	}
	if len(s.observed) >= 64 {
		for observedID := range s.observed {
			delete(s.observed, observedID)
			break
		}
	}
	s.observed[strings.ToLower(id)] = now.Add(codexResumeTTL)
	s.mu.Unlock()
	return (&url.URL{
		Scheme: "vuja",
		Host:   "codex-resume",
		Path:   "/" + id,
		RawQuery: url.Values{
			"socket": []string{s.socketPath},
			"token":  []string{s.token},
		}.Encode(),
	}).String()
}

func (s *codexResumeActionServer) Actions() <-chan *codexResumeAction {
	if s == nil {
		return nil
	}
	return s.actions
}

func (s *codexResumeActionServer) Notices() <-chan string { return s.notices }

func (s *codexResumeActionServer) serve() {
	defer close(s.done)
	defer close(s.actions)
	defer close(s.notices)
	for {
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		s.handle(connection)
	}
}

func (s *codexResumeActionServer) handle(connection *net.UnixConn) {
	defer func() { _ = connection.Close() }()
	reply := func(result codexResumeResult) {
		_ = connection.SetWriteDeadline(time.Now().Add(codexResumeActionTimeout))
		if result.err != nil {
			_, _ = fmt.Fprintf(connection, "error\t%s\n", result.err)
			select {
			case s.notices <- result.err.Error():
			default:
			}
		} else {
			_, _ = io.WriteString(connection, "ok\n")
		}
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(io.LimitReader(connection, 256)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	parts := strings.Split(strings.TrimSpace(line), "\t")
	if len(parts) != 3 || parts[0] != s.token || !isCodexResumeID(parts[1]) {
		reply(codexResumeResult{err: errors.New("invalid resume activation credentials")})
		return
	}
	id := strings.ToLower(parts[1])
	now := time.Now()
	deadlineNS, err := strconv.ParseInt(parts[2], 10, 64)
	deadline := time.Unix(0, deadlineNS)
	// Include the deadline in the request: a connection waiting in the Unix
	// backlog must not execute after its caller has stopped waiting.
	if err != nil || !deadline.After(now) || deadline.After(now.Add(codexResumeActionTimeout)) {
		reply(codexResumeResult{err: errors.New("resume activation request expired; click again at an idle prompt")})
		return
	}
	s.mu.Lock()
	expires, observed := s.observed[id]
	if !expires.After(now) {
		delete(s.observed, id)
		observed = false
	}
	s.mu.Unlock()
	if !observed {
		reply(codexResumeResult{err: errors.New("resume link expired or was already used; run the displayed resume command manually")})
		return
	}
	action := &codexResumeAction{id: id, deadline: deadline, result: make(chan codexResumeResult, 1)}
	select {
	case s.actions <- action:
	default:
		reply(codexResumeResult{err: errors.New("resume receiver is busy; click again")})
		return
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var result codexResumeResult
	select {
	case result = <-action.result:
	case <-timer.C:
		result = action.cancel(errors.New("resume receiver did not respond; click again at an idle prompt"))
	case <-s.stop:
		result = action.cancel(errors.New("Vuja session closed before resume activation"))
	}
	if result.consumed {
		s.mu.Lock()
		if s.observed[id].Equal(expires) {
			delete(s.observed, id)
		}
		s.mu.Unlock()
	}
	reply(result)
}

func (s *codexResumeActionServer) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.listener.Close()
		<-s.done
		_ = os.Remove(s.socketPath)
	})
}

func dispatchCodexResumeURL(rawURL, actionsDir string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse Codex resume URL: %w", err)
	}
	id := strings.TrimPrefix(parsed.EscapedPath(), "/")
	decodedID, err := url.PathUnescape(id)
	if err != nil || parsed.Scheme != "vuja" || parsed.Host != "codex-resume" || !isCodexResumeID(decodedID) {
		return errors.New("invalid Codex resume URL")
	}
	socketPath := filepath.Clean(parsed.Query().Get("socket"))
	allowed := pathWithinDirectory(socketPath, actionsDir)
	if runtimeDir, runtimeErr := codexResumeRuntimeActionsDir(actionsDir); runtimeErr == nil {
		allowed = allowed || pathWithinDirectory(socketPath, runtimeDir)
	}
	if !allowed {
		return errors.New("codex resume socket is outside Vuja's private action directory")
	}
	token := parsed.Query().Get("token")
	if !isCodexResumeToken(token) {
		return errors.New("invalid Codex resume action token")
	}
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to Vuja session: %w", err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
	deadline := time.Now().Add(codexResumeActionTimeout).UnixNano()
	if _, err := fmt.Fprintf(connection, "%s\t%s\t%d\n", token, strings.ToLower(decodedID), deadline); err != nil {
		return fmt.Errorf("send Codex resume action: %w", err)
	}
	// A successful socket write is not acceptance by the owning shell. Older
	// fire-and-forget servers close without a reply and must not look successful.
	_ = connection.SetReadDeadline(time.Now().Add(3 * codexResumeActionTimeout))
	reply, err := bufio.NewReader(io.LimitReader(connection, 512)).ReadString('\n')
	if err != nil {
		return errors.New("Vuja did not confirm resume activation; inspect the originating terminal")
	}
	if reply == "ok\n" {
		return nil
	}
	if message, ok := strings.CutPrefix(strings.TrimSpace(reply), "error\t"); ok {
		return fmt.Errorf("Codex resume: %s", message)
	}
	return errors.New("invalid Vuja resume activation response")
}

func codexResumeActionsDir() (string, error) {
	cacheDir, err := config.CachePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "actions"), nil
}

func pathWithinDirectory(path, directory string) bool {
	if path == "." || path == "" || directory == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(path))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isCodexResumeID(id string) bool {
	return len(id) == 36 && codexResumeIDPattern.FindString(id) == id
}

func codexResumeCommand(id string) string {
	if !isCodexResumeID(id) {
		return ""
	}
	return "codex resume " + strings.ToLower(id)
}

func codexResumeInput(id string, shellBusy bool, buffer string, cursorOffset int) []byte {
	if shellBusy || buffer != "" || cursorOffset != 0 {
		return nil
	}
	command := codexResumeCommand(id)
	if command == "" {
		return nil
	}
	return []byte(command + "\r")
}

func terminalInputEvents(reader io.Reader) <-chan []byte {
	events := make(chan []byte, 1)
	go func() {
		defer close(events)
		buffer := make([]byte, 128)
		for {
			count, err := reader.Read(buffer)
			if count > 0 {
				data := append([]byte(nil), buffer[:count]...)
				events <- data
			}
			if err != nil {
				return
			}
		}
	}()
	return events
}

func isCodexResumeToken(token string) bool {
	return len(token) == 32 && strings.IndexFunc(token, func(char rune) bool {
		return (char < '0' || char > '9') && (char < 'a' || char > 'f')
	}) < 0
}

var openURLCmd = &cobra.Command{
	Use:    "open-url <url>",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		directory, err := codexResumeActionsDir()
		if err != nil {
			return err
		}
		return dispatchCodexResumeURL(args[0], directory)
	},
}

func init() {
	rootCmd.AddCommand(openURLCmd)
}

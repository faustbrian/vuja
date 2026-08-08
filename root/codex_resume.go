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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

const (
	codexResumeLinePrefix = "To continue this session, run codex resume"
	codexResumeLineLimit  = 2048
	codexResumeTTL        = 10 * time.Minute
)

var codexResumeIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type codexResumeLinkifier struct {
	linkFor            func(string) string
	prefixMatch        int
	escapeState        codexResumeEscapeState
	captureEscapeState codexResumeEscapeState
	captureEscapeStart int
	capturing          bool
	line               []byte
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
	prefix := []byte(codexResumeLinePrefix)
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
				l.resetCapture()
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
		if char == prefix[l.prefixMatch] {
			l.prefixMatch++
			if l.prefixMatch == len(prefix) {
				l.prefixMatch = 0
				l.capturing = true
				l.captureEscapeStart = -1
			}
		} else if char == prefix[0] {
			l.prefixMatch = 1
		} else {
			l.prefixMatch = 0
		}
	}
	return output.Bytes()
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
	l.prefixMatch = 0
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
	actions    chan string
	done       chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	observed   map[string]time.Time
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
	socketPath := filepath.Join(directory, fmt.Sprintf("%d-%s.sock", os.Getpid(), token[:12]))
	if len(socketPath) >= 100 {
		return nil, fmt.Errorf("Codex resume action socket path is too long: %s", socketPath)
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
		actions: make(chan string, 4), done: make(chan struct{}), observed: make(map[string]time.Time),
	}
	go server.serve()
	return server, nil
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

func (s *codexResumeActionServer) Actions() <-chan string {
	if s == nil {
		return nil
	}
	return s.actions
}

func (s *codexResumeActionServer) serve() {
	defer close(s.done)
	defer close(s.actions)
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
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(io.LimitReader(connection, 256)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	parts := strings.Split(strings.TrimSpace(line), "\t")
	if len(parts) != 2 || parts[0] != s.token || !isCodexResumeID(parts[1]) {
		return
	}
	id := strings.ToLower(parts[1])
	now := time.Now()
	s.mu.Lock()
	expires, observed := s.observed[id]
	if observed && expires.After(now) {
		delete(s.observed, id)
	} else {
		delete(s.observed, id)
		observed = false
	}
	s.mu.Unlock()
	if !observed {
		return
	}
	select {
	case s.actions <- id:
	default:
	}
}

func (s *codexResumeActionServer) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
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
	if !pathWithinDirectory(socketPath, actionsDir) {
		return errors.New("Codex resume socket is outside Vuja's private action directory")
	}
	token := parsed.Query().Get("token")
	if !isCodexResumeToken(token) {
		return errors.New("invalid Codex resume action token")
	}
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return fmt.Errorf("connect to Vuja session: %w", err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := fmt.Fprintf(connection, "%s\t%s\n", token, strings.ToLower(decodedID)); err != nil {
		return fmt.Errorf("send Codex resume action: %w", err)
	}
	return nil
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

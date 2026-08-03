package root

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/text/unicode/norm"
)

const (
	terminalSyncStart             = "\x1b[?2026h"
	terminalSyncEnd               = "\x1b[?2026l"
	terminalBracketedPasteEnable  = "\x1b[?2004h"
	terminalBracketedPasteDisable = "\x1b[?2004l"

	terminalModelScrollback        = 256
	terminalInputHorizontalPadding = 2
	terminalModelChunkSize         = 8 * 1024
)

var statusVersionOrder = [...]string{
	"laravel", "php", "composer",
	"python", "ruby", "elixir",
	"node", "bun", "go", "rust",
	"docker-compose", "docker",
}

type terminalPhase uint8

const (
	terminalOutput terminalPhase = iota
	terminalPrompt
	terminalInput
)

type terminalCompositor struct {
	mu sync.Mutex

	out            io.Writer
	asyncOut       *asyncTerminalWriter
	enabled        bool
	layout         bool
	awaitingPrompt bool
	marker         string
	width          int
	height         int
	closed         bool

	emulator        *vt.Emulator
	emulatorDone    <-chan struct{}
	backdrop        *vt.Emulator
	backdropDone    <-chan struct{}
	modelUTF8Tail   []byte
	modelCSITail    []byte
	modelRecoveries uint64
	stream          terminalMarkerStream
	phase           terminalPhase

	promptStartAbs          int
	surfaceBottomAbs        int
	surfaceRows             int
	surfaceContentRows      int
	surfaceContentLines     []string
	surfaceContentCells     []uv.Line
	renderedTop             int
	renderedLines           []string
	pendingLineBreak        bool
	multilineInput          bool
	pendingRedraw           []byte
	promptPrelude           string
	transientGeometryDirty  bool
	clearTransientUI        func() []byte
	setTransientUIGeometry  func(bool, int)
	renderTransientUI       func() []byte
	disableTransientUI      func() []byte
	transientUIRegion       func() (int, int)
	transientUIVisible      func() bool
	inputBoxTheme           terminalInputBoxTheme
	inputBoxBackgroundCode  string
	inputBoxSurfaceCode     string
	completedSurfaceCode    string
	inputBoxStatusCode      string
	inputBoxStatusTextCode  string
	inputBoxPath            string
	lastExitStatus          *int
	chatboxConfig           terminalChatboxConfig
	chatboxConfigured       bool
	statusSnapshot          statusSnapshot
	titleLinesCache         []string
	statusLinesCache        []string
	chatboxColorCodes       map[string]string
	commandContext          string
	commandCardOpen         bool
	commandOutputDirect     bool
	commandOutputLineOpen   bool
	commandOutputPositioned bool
	commandOutputStarted    bool
	commandOutputColumn     int
	historySpacingRows      int
	commandStartedAt        time.Time
	commandDirectory        string
	commandStatusSnapshot   statusSnapshot
	lastCommandSnapshot     statusSnapshot
	hasLastCommandSnapshot  bool
	now                     func() time.Time
}

type asyncTerminalWriter struct {
	out     io.Writer
	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	closed  bool
	done    chan struct{}
	once    sync.Once
}

func newAsyncTerminalWriter(out io.Writer) *asyncTerminalWriter {
	writer := &asyncTerminalWriter{out: out, done: make(chan struct{})}
	writer.cond = sync.NewCond(&writer.mu)
	go writer.run()
	return writer
}

func (w *asyncTerminalWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	w.pending = append(w.pending, data...)
	w.cond.Signal()
	w.mu.Unlock()
	return len(data), nil
}

func (w *asyncTerminalWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for len(w.pending) == 0 && !w.closed {
			w.cond.Wait()
		}
		if len(w.pending) == 0 && w.closed {
			w.mu.Unlock()
			return
		}
		batch := w.pending
		w.pending = nil
		w.mu.Unlock()
		for len(batch) > 0 {
			written, err := w.out.Write(batch)
			if err != nil || written <= 0 {
				break
			}
			batch = batch[written:]
		}
	}
}

func (w *asyncTerminalWriter) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	<-w.done
}

type terminalInputBoxTheme struct {
	Background                 string
	SurfaceBackground          string
	CompletedSurfaceBackground string
	StatusBackground           string
	StatusText                 string
	Border                     string
	Accent                     string
	Muted                      string
}

type terminalChatboxConfig struct {
	Prompt            string
	Separator         string
	Scrollback        string
	PathColorMode     string
	PathMaxSegments   int
	HistorySpacing    int
	Title             terminalChatboxBarConfig
	Status            terminalChatboxBarConfig
	Colors            map[string]string
	CollapseVersions  bool
	Density           string
	Responsive        bool
	SnapshotMetadata  string
	CompletedCommand  string
	Metrics           string
	Versions          string
	VersionAllow      []string
	VersionDeny       []string
	DockerContext     string
	KubernetesContext string
	AWSContext        string
	DurationFast      time.Duration
	DurationSlow      time.Duration
	CPUAverage        int
	CPUHigh           int
	CPUCritical       int
	MemoryAverage     int
	MemoryHigh        int
	MemoryCritical    int
}

type terminalChatboxBarConfig struct {
	Left   []string
	Center []string
	Right  []string
}

type terminalStatusAlignment uint8

const (
	terminalStatusLeft terminalStatusAlignment = iota
	terminalStatusCenter
	terminalStatusRight
)

type terminalStatusSegment struct {
	name      string
	text      string
	priority  int
	alignment terminalStatusAlignment
}

type terminalUIPresenter struct {
	mu      sync.Mutex
	compose func(func() []byte)
}

func newTerminalUIPresenter(compose func(func() []byte)) *terminalUIPresenter {
	return &terminalUIPresenter{compose: compose}
}

func (p *terminalUIPresenter) Update(update func(compose func(func() []byte))) {
	if p == nil || p.compose == nil || update == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	update(p.compose)
}

func (p *terminalUIPresenter) Present(render func() []byte) {
	p.Update(func(compose func(func() []byte)) {
		compose(render)
	})
}

func newTerminalCompositor(out io.Writer, promptPosition, marker string, width, height int) *terminalCompositor {
	bottomLayout := promptPosition == "bottom" && marker != "" && width > 0 && height > 1
	var asyncOut *asyncTerminalWriter
	if file, ok := out.(*os.File); ok && file.Fd() == os.Stdout.Fd() {
		asyncOut = newAsyncTerminalWriter(out)
		out = asyncOut
	}
	c := &terminalCompositor{
		out:            out,
		asyncOut:       asyncOut,
		enabled:        bottomLayout,
		layout:         bottomLayout,
		awaitingPrompt: bottomLayout,
		marker:         marker,
		width:          width,
		height:         height,
		phase:          terminalOutput,
		now:            time.Now,
	}
	if c.enabled {
		c.resetEmulator(width, height)
		c.resetBackdrop(width, height)
		c.stream.prefix = "777;vuja;" + marker + ";"
	}
	return c
}

func (c *terminalCompositor) WritePTY(data []byte) {
	for len(data) > terminalModelChunkSize {
		c.writePTYChunk(data[:terminalModelChunkSize])
		data = data[terminalModelChunkSize:]
	}
	c.writePTYChunk(data)
}

func (c *terminalCompositor) writePTYChunk(data []byte) {
	if len(data) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	if !c.enabled {
		c.writeTerminal(data)
		return
	}

	dirtyInput := false
	c.stream.Consume(data, func(text []byte) {
		if len(text) == 0 {
			return
		}
		completingPromptRedraw := len(c.pendingRedraw) > 0
		if len(c.pendingRedraw) > 0 {
			combined := make([]byte, 0, len(c.pendingRedraw)+len(text))
			combined = append(combined, c.pendingRedraw...)
			combined = append(combined, text...)
			c.pendingRedraw = c.pendingRedraw[:0]
			text = combined
		}
		if !c.layout {
			c.writeTerminal(text)
			return
		}
		if c.phase == terminalInput &&
			(isPromptRedrawPrelude(text) || endsWithIncompleteCSI(text)) &&
			len(text) <= 4096 {
			c.pendingRedraw = append(c.pendingRedraw, text...)
			return
		}
		if c.phase == terminalInput && !c.multilineInput && !completingPromptRedraw {
			if newline := lastLineBreak(text); newline >= 0 {
				lineBreak := text[:newline+1]
				if strings.TrimSpace(ansi.Strip(string(lineBreak))) != "" {
					c.renderBackground(lineBreak)
				}
				c.pendingLineBreak = true
				text = text[newline+1:]
				if len(text) == 0 {
					return
				}
			}
		}
		screenReset := c.phase != terminalOutput && resetsTerminalViewport(text)
		if c.phase != terminalOutput {
			if sideEffects := terminalOSCSequences(text); len(sideEffects) > 0 {
				c.writeTerminal(sideEffects)
			}
		}
		c.writeModel(text)
		if screenReset {
			c.promptStartAbs = c.emulator.ScrollbackLen()
			c.surfaceBottomAbs = c.absoluteCursorLine()
			c.renderedLines = nil
			c.pendingLineBreak = false
		}
		switch c.phase {
		case terminalOutput:
			lineOriented := c.commandOutputLineOpen || bytes.IndexByte(text, '\n') >= 0
			switch {
			case !c.commandCardOpen:
				c.writeDirectCommandOutput(text)
			case requiresCommandPassThrough(text):
				c.commandOutputDirect = true
				c.writeDirectCommandOutput(text)
			case c.commandOutputDirect || !lineOriented:
				c.writeDirectCommandOutput(text)
			default:
				c.writePaddedCommandOutput(text)
			}
		case terminalPrompt, terminalInput:
			c.updateSurfaceBottom()
			dirtyInput = c.phase == terminalInput
		}
	}, func(event string) {
		if !c.layout {
			return
		}
		switch {
		case event == "prompt-start":
			c.flushPendingRedrawToModel()
			if c.phase == terminalInput && c.pendingLineBreak {
				c.enterCommandOutput()
				c.phase = terminalOutput
			}
			c.beginPrompt(false)
			dirtyInput = false
		case event == "continuation-start":
			c.flushPendingRedrawToModel()
			c.beginPrompt(true)
			dirtyInput = false
		case event == "prompt-end", event == "continuation-end":
			c.phase = terminalInput
			c.awaitingPrompt = false
			c.updateSurfaceBottom()
			c.writeTerminal([]byte(terminalBracketedPasteEnable))
			dirtyInput = true
		case event == "command-start":
			c.multilineInput = false
			c.writeTerminal([]byte(terminalBracketedPasteDisable))
			c.startCommandExecution()
			if len(c.pendingRedraw) > 0 {
				c.writeModel(c.pendingRedraw)
				c.writeTerminal(c.pendingRedraw)
				c.pendingRedraw = c.pendingRedraw[:0]
			}
			if c.phase != terminalOutput {
				c.enterCommandOutput()
			}
			c.phase = terminalOutput
			dirtyInput = false
		case strings.HasPrefix(event, "command-end"):
			var exitCode *int
			if value, ok := strings.CutPrefix(event, "command-end:"); ok && !c.commandStartedAt.IsZero() {
				if status, err := strconv.Atoi(value); err == nil {
					c.lastExitStatus = &status
					exitCode = &status
					c.titleLinesCache = nil
					c.statusLinesCache = nil
				}
			}
			c.finishCommandOutput(exitCode)
			c.phase = terminalOutput
		}
	})

	if c.layout && dirtyInput && c.phase == terminalInput {
		c.renderPinned()
	}
}

func (c *terminalCompositor) flushPendingRedrawToModel() {
	if len(c.pendingRedraw) == 0 {
		return
	}
	c.writeModel(c.pendingRedraw)
	c.pendingRedraw = c.pendingRedraw[:0]
}

func (c *terminalCompositor) renderBackground(data []byte) {
	if len(data) == 0 || c.surfaceRows == 0 {
		return
	}
	outputRows := c.height - c.surfaceRows
	if outputRows < 1 {
		return
	}

	var frame strings.Builder
	frame.Grow(len(data) + 64)
	frame.WriteString(terminalSyncStart)
	oldTop, oldRows := c.currentTransientUIRegion()
	if c.clearTransientUI != nil {
		frame.Write(c.clearTransientUI())
	}
	frame.Write(c.restoreBackdropRows(oldTop, oldRows))
	baseStart := frame.Len()
	frame.WriteString("\x1b7\x1b[?25l")
	fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[%d;1H", outputRows, outputRows)
	frame.Write(data)
	frame.WriteString("\x1b8\x1b[?25h")
	baseEnd := frame.Len()
	if c.renderTransientUI != nil {
		frame.Write(c.renderTransientUI())
	}
	frame.WriteString(terminalSyncEnd)
	contents := frame.String()
	_, _ = io.WriteString(c.out, contents)
	c.recordBackdrop([]byte(contents[baseStart:baseEnd]))
}

func (c *terminalCompositor) WriteUI(data []byte) {
	if len(data) == 0 {
		return
	}
	c.ComposeUI(func() []byte { return data })
}

func (c *terminalCompositor) ComposeUI(render func() []byte) {
	if render == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.enabled && (!c.layout || c.awaitingPrompt || c.phase != terminalInput) {
		return
	}
	oldTop, oldRows := c.currentTransientUIRegion()
	data := render()
	if len(data) == 0 {
		return
	}
	if c.enabled {
		var frame strings.Builder
		frame.Grow(len(data) + oldRows*(c.width+24) + len(terminalSyncStart) + len(terminalSyncEnd))
		frame.WriteString(terminalSyncStart)
		if c.transientUIVisible != nil && c.transientUIVisible() {
			frame.Write(c.restoreBackdropRows(oldTop, oldRows))
			frame.Write(data)
		} else {
			frame.Write(data)
			frame.Write(c.restoreBackdropRows(oldTop, oldRows))
		}
		frame.WriteString(terminalSyncEnd)
		_, _ = io.WriteString(c.out, frame.String())
		return
	}
	c.writeTerminal(data)
}

func (c *terminalCompositor) SetTransientUIReflow(
	clear func() []byte,
	setGeometry func(bool, int),
	render func() []byte,
	disable func() []byte,
	region func() (int, int),
	visible func() bool,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.clearTransientUI = clear
	c.setTransientUIGeometry = setGeometry
	c.renderTransientUI = render
	c.disableTransientUI = disable
	c.transientUIRegion = region
	c.transientUIVisible = visible
}

func (c *terminalCompositor) SetInputBoxTheme(theme terminalInputBoxTheme) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.inputBoxTheme = theme
	c.inputBoxBackgroundCode = terminalTrueColor("48", theme.Background)
	c.inputBoxSurfaceCode = terminalTrueColor("48", theme.SurfaceBackground)
	c.completedSurfaceCode = terminalTrueColor("48", theme.CompletedSurfaceBackground)
	c.inputBoxStatusCode = terminalTrueColor("48", theme.StatusBackground)
	c.inputBoxStatusTextCode = terminalTrueColor("38", theme.StatusText)
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	c.transientGeometryDirty = true
	c.renderedLines = nil
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func (c *terminalCompositor) SetChatboxConfig(chatbox terminalChatboxConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.chatboxConfig = terminalChatboxConfig{
		Prompt:            chatbox.Prompt,
		Separator:         chatbox.Separator,
		Scrollback:        chatbox.Scrollback,
		PathColorMode:     chatbox.PathColorMode,
		PathMaxSegments:   chatbox.PathMaxSegments,
		HistorySpacing:    chatbox.HistorySpacing,
		Title:             cloneTerminalChatboxBar(chatbox.Title),
		Status:            cloneTerminalChatboxBar(chatbox.Status),
		Colors:            cloneVersions(chatbox.Colors),
		CollapseVersions:  chatbox.CollapseVersions,
		Density:           chatbox.Density,
		Responsive:        chatbox.Responsive,
		SnapshotMetadata:  chatbox.SnapshotMetadata,
		CompletedCommand:  chatbox.CompletedCommand,
		Metrics:           chatbox.Metrics,
		Versions:          chatbox.Versions,
		VersionAllow:      append([]string(nil), chatbox.VersionAllow...),
		VersionDeny:       append([]string(nil), chatbox.VersionDeny...),
		DockerContext:     chatbox.DockerContext,
		KubernetesContext: chatbox.KubernetesContext,
		AWSContext:        chatbox.AWSContext,
		DurationFast:      chatbox.DurationFast,
		DurationSlow:      chatbox.DurationSlow,
		CPUAverage:        chatbox.CPUAverage,
		CPUHigh:           chatbox.CPUHigh,
		CPUCritical:       chatbox.CPUCritical,
		MemoryAverage:     chatbox.MemoryAverage,
		MemoryHigh:        chatbox.MemoryHigh,
		MemoryCritical:    chatbox.MemoryCritical,
	}
	c.chatboxColorCodes = make(map[string]string, len(chatbox.Colors))
	for name, color := range chatbox.Colors {
		c.chatboxColorCodes[name] = terminalTrueColor("38", color)
	}
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	c.chatboxConfigured = true
	c.renderedLines = nil
	c.transientGeometryDirty = true
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func cloneTerminalChatboxBar(bar terminalChatboxBarConfig) terminalChatboxBarConfig {
	return terminalChatboxBarConfig{
		Left:   append([]string(nil), bar.Left...),
		Center: append([]string(nil), bar.Center...),
		Right:  append([]string(nil), bar.Right...),
	}
}

func (c *terminalCompositor) SetStatusSnapshot(snapshot statusSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	snapshot = cloneStatusSnapshot(snapshot)
	if c.statusSnapshot.Revision > 0 && snapshot.Revision < c.statusSnapshot.Revision {
		return
	}
	if statusSnapshotsEqual(c.statusSnapshot, snapshot) {
		c.statusSnapshot.Revision = max(c.statusSnapshot.Revision, snapshot.Revision)
		return
	}
	c.statusSnapshot = snapshot
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	if snapshot.Directory != "" {
		c.inputBoxPath = displayTerminalPath(snapshot.Directory)
	}
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func statusSnapshotsEqual(left, right statusSnapshot) bool {
	if left.Directory != right.Directory || left.CommandContext != right.CommandContext || left.RepositoryRoot != right.RepositoryRoot ||
		left.DirectoryReadOnly != right.DirectoryReadOnly || left.Git != right.Git ||
		left.Package != right.Package || left.Shell != right.Shell || left.Session != right.Session ||
		left.Contexts != right.Contexts || !left.StaleSince.Equal(right.StaleSince) || left.CPU != right.CPU ||
		left.Memory != right.Memory || left.HasCPU != right.HasCPU || left.HasMemory != right.HasMemory ||
		left.Duration != right.Duration || !maps.Equal(left.Versions, right.Versions) ||
		!maps.Equal(left.ActiveVersions, right.ActiveVersions) ||
		!slices.Equal(left.Environment, right.Environment) ||
		!slices.Equal(left.VersionMismatches, right.VersionMismatches) ||
		!slices.Equal(left.StaleProviders, right.StaleProviders) {
		return false
	}
	if left.ExitCode == nil || right.ExitCode == nil {
		return left.ExitCode == nil && right.ExitCode == nil
	}
	return *left.ExitCode == *right.ExitCode
}

func cloneStatusSnapshot(snapshot statusSnapshot) statusSnapshot {
	snapshot.Versions = cloneVersions(snapshot.Versions)
	snapshot.ActiveVersions = cloneVersions(snapshot.ActiveVersions)
	snapshot.Environment = append([]string(nil), snapshot.Environment...)
	snapshot.VersionMismatches = append([]versionMismatch(nil), snapshot.VersionMismatches...)
	snapshot.StaleProviders = append([]string(nil), snapshot.StaleProviders...)
	if snapshot.ExitCode != nil {
		exitCode := *snapshot.ExitCode
		snapshot.ExitCode = &exitCode
	}
	return snapshot
}

func (c *terminalCompositor) SetCommandContext(command string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	next := statusCommandContext(command)
	if next == c.commandContext {
		return
	}
	c.commandContext = next
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func statusCommandContext(command string) string {
	switch strings.TrimSpace(command) {
	case "kubernetes", "aws", "docker":
		return strings.TrimSpace(command)
	}
	fields := strings.Fields(strings.TrimSpace(command))
	for len(fields) > 0 {
		token := fields[0]
		if strings.Contains(token, "=") && !strings.HasPrefix(token, "=") {
			fields = fields[1:]
			continue
		}
		switch filepath.Base(token) {
		case "command", "env":
			fields = fields[1:]
			continue
		case "sudo":
			fields = fields[1:]
			for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
				fields = fields[1:]
			}
			continue
		}
		switch filepath.Base(token) {
		case "kubectl", "helm", "kubens", "kubectx", "oc":
			return "kubernetes"
		case "aws", "sam", "cdk":
			return "aws"
		case "docker", "docker-compose":
			return "docker"
		default:
			return ""
		}
	}
	return ""
}

func (c *terminalCompositor) SetInputBoxPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	nextPath := displayTerminalPath(path)
	if c.inputBoxPath == nextPath {
		return
	}
	c.inputBoxPath = nextPath
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	c.renderedLines = nil
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func (c *terminalCompositor) SetMultilineInput(multiline bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.multilineInput = multiline
}

func (c *terminalCompositor) WriteNotification(data []byte) {
	if len(data) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.layout && c.phase != terminalOutput && c.surfaceRows > 0 {
		c.renderBackground(data)
		return
	}
	c.writeTerminal(data)
}

func (c *terminalCompositor) Resize(width, height int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || !c.enabled {
		return
	}
	if width <= 0 || height <= 1 {
		if c.layout {
			var frame strings.Builder
			frame.WriteString(terminalSyncStart)
			c.clearAndDisableTransientUI(&frame)
			frame.WriteString("\x1b[r\x1b[?7h\x1b[?25h")
			frame.WriteString(terminalSyncEnd)
			_, _ = io.WriteString(c.out, frame.String())
		}
		c.layout = false
		c.awaitingPrompt = true
		c.surfaceRows = 0
		c.surfaceContentRows = 0
		c.surfaceContentLines = nil
		c.surfaceContentCells = nil
		c.renderedLines = nil
		c.pendingRedraw = c.pendingRedraw[:0]
		c.transientGeometryDirty = false
		c.phase = terminalOutput
		c.commandCardOpen = false
		c.commandOutputDirect = false
		c.commandOutputLineOpen = false
		c.commandOutputPositioned = false
		c.commandOutputStarted = false
		c.commandOutputColumn = 0
		c.commandStartedAt = time.Time{}
		c.commandDirectory = ""
		c.commandStatusSnapshot = statusSnapshot{}
		return
	}
	if !c.layout {
		c.closeEmulator()
		c.closeBackdrop()
		c.resetEmulator(width, height)
		c.resetBackdrop(width, height)
		c.layout = true
		c.awaitingPrompt = true
		c.width = width
		c.height = height
		c.titleLinesCache = nil
		c.statusLinesCache = nil
		c.pendingRedraw = c.pendingRedraw[:0]
		c.transientGeometryDirty = true
		c.phase = terminalOutput
		c.commandCardOpen = false
		c.commandOutputDirect = false
		c.commandOutputLineOpen = false
		c.commandOutputPositioned = false
		c.commandOutputStarted = false
		c.commandOutputColumn = 0
		c.commandStartedAt = time.Time{}
		c.commandDirectory = ""
		c.commandStatusSnapshot = statusSnapshot{}
		return
	}
	oldRows := c.surfaceContentRows
	c.width = width
	c.height = height
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	c.transientGeometryDirty = true
	c.emulator.Resize(width, height)
	// x/vt currently retains scroll margins across resize even when their old
	// bounds no longer fit the resized buffer. Real terminals clamp or reset
	// those margins, so normalize the shadow model while preserving its cursor.
	_, _ = c.emulator.Write([]byte("\x1b7\x1b[r\x1b[?69l\x1b8"))
	c.modelCSITail = c.modelCSITail[:0]
	if c.backdrop != nil {
		c.backdrop.Resize(width, height)
	}
	cursorAbs := c.absoluteCursorLine()
	c.promptStartAbs = max(c.emulator.ScrollbackLen(), cursorAbs-max(oldRows-1, 0))
	c.surfaceBottomAbs = max(cursorAbs, c.promptStartAbs+oldRows-1)
	c.renderedLines = nil
	if c.phase == terminalInput {
		c.renderPinned()
	}
}

func (c *terminalCompositor) PromptRows() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.layout {
		return 0
	}
	return c.surfaceRows
}

func (c *terminalCompositor) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled && c.layout && !c.awaitingPrompt && !c.closed
}

func (c *terminalCompositor) Managed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled && !c.closed
}

func (c *terminalCompositor) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.enabled {
		var frame strings.Builder
		frame.WriteString(terminalSyncStart)
		c.clearAndDisableTransientUI(&frame)
		frame.WriteString(terminalBracketedPasteDisable)
		frame.WriteString("\x1b7\x1b[r\x1b8\x1b[?7h\x1b[?25h")
		frame.WriteString(terminalSyncEnd)
		_, _ = io.WriteString(c.out, frame.String())
		c.closeEmulator()
		c.closeBackdrop()
	}
	asyncOut := c.asyncOut
	c.mu.Unlock()
	if asyncOut != nil {
		asyncOut.Close()
	}
}

func (c *terminalCompositor) resetEmulator(width, height int) {
	c.emulator = vt.NewEmulator(width, height)
	c.emulator.SetScrollbackSize(terminalModelScrollback)
	c.modelUTF8Tail = c.modelUTF8Tail[:0]
	c.modelCSITail = c.modelCSITail[:0]
	done := make(chan struct{})
	c.emulatorDone = done
	go func(emulator *vt.Emulator) {
		_, _ = io.Copy(io.Discard, emulator)
		close(done)
	}(c.emulator)
}

func (c *terminalCompositor) resetBackdrop(width, height int) {
	c.backdrop = vt.NewEmulator(width, height)
	done := make(chan struct{})
	c.backdropDone = done
	go func(backdrop *vt.Emulator) {
		_, _ = io.Copy(io.Discard, backdrop)
		close(done)
	}(c.backdrop)
}

func (c *terminalCompositor) closeBackdrop() {
	if c.backdrop == nil {
		return
	}
	if closer, ok := c.backdrop.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}
	if c.backdropDone != nil {
		<-c.backdropDone
	}
	_ = c.backdrop.Close()
	c.backdrop = nil
	c.backdropDone = nil
}

func (c *terminalCompositor) writeTerminal(data []byte) {
	if len(data) == 0 {
		return
	}
	_, _ = c.out.Write(data)
	if c.backdrop != nil {
		_, _ = c.backdrop.Write(data)
	}
}

func (c *terminalCompositor) recordBackdrop(data []byte) {
	if c.backdrop != nil && len(data) > 0 {
		_, _ = c.backdrop.Write(data)
	}
}

func (c *terminalCompositor) currentTransientUIRegion() (int, int) {
	if c.transientUIRegion == nil {
		return 0, 0
	}
	top, rows := c.transientUIRegion()
	if rows <= 0 || top >= c.height {
		return 0, 0
	}
	top = clamp(top, 0, c.height-1)
	return top, min(rows, c.height-top)
}

func (c *terminalCompositor) restoreBackdropRows(top, rows int) []byte {
	if c.backdrop == nil || rows <= 0 || c.width <= 0 || c.height <= 0 {
		return nil
	}
	bottom := min(top+rows, c.height)
	var restored strings.Builder
	restored.Grow((bottom - top) * (c.width + 24))
	restored.WriteString("\x1b[?25l\x1b[?7l")
	for row := top; row < bottom; row++ {
		line := uv.NewLine(c.width)
		for column := 0; column < c.width; column++ {
			if cell := c.backdrop.CellAt(column, row); cell != nil {
				line.Set(column, cell)
			}
		}
		fmt.Fprintf(&restored, "\x1b[%d;1H\x1b[2K%s\x1b[0m", row+1, line.Render())
	}
	cursor := c.backdrop.CursorPosition()
	fmt.Fprintf(&restored, "\x1b[%d;%dH\x1b[?7h\x1b[?25h", cursor.Y+1, cursor.X+1)
	return []byte(restored.String())
}

func (c *terminalCompositor) closeEmulator() {
	if c.emulator == nil {
		return
	}
	if closer, ok := c.emulator.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}
	if c.emulatorDone != nil {
		<-c.emulatorDone
	}
	_ = c.emulator.Close()
	c.emulator = nil
	c.emulatorDone = nil
}

func (c *terminalCompositor) beginPrompt(continuation bool) {
	if continuation && c.phase == terminalInput {
		if c.pendingLineBreak {
			c.writeModel([]byte("\r\n"))
			c.pendingLineBreak = false
		}
		c.phase = terminalPrompt
		c.updateSurfaceBottom()
		return
	}
	if c.phase == terminalOutput {
		if c.historySpacingRows > 0 {
			spacing := strings.Repeat("\r\n", c.historySpacingRows)
			c.writeModel([]byte(spacing))
			c.writeTerminal([]byte(spacing))
			c.historySpacingRows = 0
		}
		preludeRows := c.inputBoxDecorationRows()
		if c.emulator.CursorPosition().X > 0 {
			preludeRows++
		}
		if preludeRows > 0 {
			c.promptPrelude = strings.Repeat("\r\n", preludeRows)
			c.writeModel([]byte(c.promptPrelude))
		}
	}
	c.phase = terminalPrompt
	c.promptStartAbs = c.absoluteCursorLine()
	c.surfaceBottomAbs = c.promptStartAbs
	c.renderedLines = nil
}

func (c *terminalCompositor) enterCommandOutput() {
	c.startCommandExecution()
	if c.chatboxConfig.Scrollback == "output" {
		c.enterRawCommandOutput()
		return
	}
	var frame strings.Builder
	frame.WriteString(terminalSyncStart)
	oldTop, oldRows := c.currentTransientUIRegion()
	c.clearAndDisableTransientUI(&frame)
	frame.Write(c.restoreBackdropRows(oldTop, oldRows))
	baseStart := frame.Len()
	frame.WriteString("\x1b[?25l\x1b[r")
	if c.surfaceRows > 0 {
		cursor := c.emulator.CursorPosition()
		cursorAbs := c.absoluteCursorLine()
		sourceStartAbs := c.surfaceBottomAbs - c.surfaceContentRows + 1
		targetTop := c.height - c.surfaceRows
		contentTop := targetTop
		completedContextRows := 0
		if c.inputBoxDecorationRows() > 0 {
			showMetadata := c.completedSnapshotMetadataVisible()
			if c.inputBoxTitleEnabled() && showMetadata {
				frame.WriteString(fmt.Sprintf("\x1b[%d;1H\x1b[2K%s\x1b[0m", contentTop+1, c.completedExecutionHeaderLine()))
				contentTop++
			}
			frame.WriteString(fmt.Sprintf("\x1b[%d;1H%s\x1b[0m", contentTop+1, c.completedInputBoxPaddingLine()))
			contentTop++
			for offset, line := range c.surfaceContentLines {
				completedLine := line
				if offset < len(c.surfaceContentCells) {
					completedLine = c.completedSurfaceContentLine(c.surfaceContentCells[offset])
				}
				fmt.Fprintf(&frame, "\x1b[%d;1H%s\x1b[0m", contentTop+offset+1, c.completedInputBoxContent(completedLine))
			}
			bottomPaddingRow := contentTop + c.surfaceContentRows
			fmt.Fprintf(&frame, "\x1b[%d;1H%s\x1b[0m", bottomPaddingRow+1, c.completedInputBoxPaddingLine())
			completedBottomRow := bottomPaddingRow
			var completedContextLines []string
			if c.completedCommandMode() == "snapshot" && showMetadata {
				completedContextLines = c.completedExecutionContextLines()
			}
			completedContextRows = len(completedContextLines)
			for _, line := range completedContextLines {
				completedBottomRow++
				fmt.Fprintf(&frame, "\x1b[%d;1H%s\x1b[0m", completedBottomRow+1, line)
			}
			for row := completedBottomRow + 1; row < c.height; row++ {
				fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row+1)
			}
			c.commandCardOpen = true
			c.commandOutputDirect = false
			c.commandOutputLineOpen = false
			c.commandOutputPositioned = c.pendingLineBreak
			c.commandOutputStarted = false
			c.commandOutputColumn = 0
		} else {
			for row := targetTop; row < c.height; row++ {
				fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row+1)
			}
			contentTop = c.height - c.surfaceContentRows
			for offset, line := range c.surfaceContentLines {
				fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K%s\x1b[0m", contentTop+offset+1, line)
			}
		}
		targetRow := contentTop + cursorAbs - sourceStartAbs
		targetRow = clamp(targetRow, contentTop, contentTop+c.surfaceContentRows-1)
		cursorColumn := cursor.X + 1
		if c.inputBoxDecorationRows() > 0 {
			cursorColumn += terminalInputHorizontalPadding
		}
		if c.pendingLineBreak {
			if c.inputBoxDecorationRows() > 0 {
				targetRow = contentTop + c.surfaceContentRows + completedContextRows
				cursorColumn = 1
			}
			fmt.Fprintf(&frame, "\x1b[%d;%dH", targetRow+1, clamp(cursorColumn, 1, c.width-1))
			frame.WriteString("\r\n")
		} else {
			fmt.Fprintf(&frame, "\x1b[%d;%dH", targetRow+1, clamp(cursorColumn, 1, c.width-1))
		}
	}
	frame.WriteString("\x1b[?25h")
	baseEnd := frame.Len()
	frame.WriteString(terminalSyncEnd)
	contents := frame.String()
	_, _ = io.WriteString(c.out, contents)
	c.recordBackdrop([]byte(contents[baseStart:baseEnd]))
	if c.pendingLineBreak {
		c.writeModel([]byte("\r\n"))
		c.pendingLineBreak = false
	}
	c.surfaceRows = 0
	c.surfaceContentRows = 0
	c.surfaceContentLines = nil
	c.surfaceContentCells = nil
	c.renderedLines = nil
	c.transientGeometryDirty = true
	c.promptPrelude = ""
}

func (c *terminalCompositor) enterRawCommandOutput() {
	var frame strings.Builder
	frame.WriteString(terminalSyncStart)
	oldTop, oldRows := c.currentTransientUIRegion()
	c.clearAndDisableTransientUI(&frame)
	frame.Write(c.restoreBackdropRows(oldTop, oldRows))
	baseStart := frame.Len()
	frame.WriteString("\x1b[?25l\x1b[r")
	targetRow := clamp(c.height-max(c.surfaceRows, 1), 0, c.height-1)
	for row := targetRow; row < c.height; row++ {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row+1)
	}
	fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[?25h", targetRow+1)
	baseEnd := frame.Len()
	frame.WriteString(terminalSyncEnd)
	contents := frame.String()
	_, _ = io.WriteString(c.out, contents)
	c.recordBackdrop([]byte(contents[baseStart:baseEnd]))
	if c.pendingLineBreak {
		c.writeModel([]byte("\r\n"))
		c.pendingLineBreak = false
	}
	c.surfaceRows = 0
	c.surfaceContentRows = 0
	c.surfaceContentLines = nil
	c.surfaceContentCells = nil
	c.renderedLines = nil
	c.transientGeometryDirty = true
	c.promptPrelude = ""
	c.commandCardOpen = false
	c.commandOutputDirect = false
	c.commandOutputLineOpen = false
	c.commandOutputPositioned = false
	c.commandOutputStarted = false
	c.commandOutputColumn = 0
}

func (c *terminalCompositor) startCommandExecution() {
	if !c.commandStartedAt.IsZero() {
		return
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.commandStartedAt = now()
	c.commandDirectory = c.inputBoxPath
	c.commandStatusSnapshot = cloneStatusSnapshot(c.statusSnapshot)
	c.commandStatusSnapshot.CommandContext = c.commandContext
	if c.commandStatusSnapshot.Directory == "" {
		c.commandStatusSnapshot.Directory = c.commandDirectory
	}
}

func (c *terminalCompositor) finishCommandOutput(exitCode *int) {
	if c.commandCardOpen {
		var completed strings.Builder
		modelLineBreaks := 0
		if !c.commandOutputStarted && !c.commandOutputPositioned {
			completed.WriteString("\r\n\r\n")
			modelLineBreaks += 2
		} else if c.commandOutputLineOpen {
			completed.WriteString("\r\n")
			modelLineBreaks++
		}
		if c.inputBoxStatusEnabled() && exitCode != nil && c.completedCommandMode() != "command" {
			now := time.Now
			if c.now != nil {
				now = c.now
			}
			duration := max(now().Sub(c.commandStartedAt), 0)
			for _, line := range c.completedExecutionOutcomeLines(duration, *exitCode) {
				completed.WriteString(line)
				completed.WriteString("\x1b[0m\r\n")
				modelLineBreaks++
			}
		}
		spacing := max(c.chatboxConfig.HistorySpacing, 0)
		completed.WriteString(strings.Repeat("\r\n", spacing))
		modelLineBreaks += spacing
		data := []byte(completed.String())
		c.writeModel([]byte(strings.Repeat("\r\n", modelLineBreaks)))
		c.writeTerminal(data)
	}
	if !c.commandCardOpen && c.chatboxConfig.Scrollback == "output" {
		c.historySpacingRows = max(c.chatboxConfig.HistorySpacing, 0)
		if c.commandOutputLineOpen {
			c.historySpacingRows++
		}
	}
	if c.commandStatusSnapshot.Directory != "" {
		c.lastCommandSnapshot = cloneStatusSnapshot(c.commandStatusSnapshot)
		c.hasLastCommandSnapshot = true
	}
	c.commandCardOpen = false
	c.commandOutputDirect = false
	c.commandOutputLineOpen = false
	c.commandOutputPositioned = false
	c.commandOutputStarted = false
	c.commandOutputColumn = 0
	c.commandStartedAt = time.Time{}
	c.commandDirectory = ""
	c.commandStatusSnapshot = statusSnapshot{}
}

func (c *terminalCompositor) completedSnapshotMetadataVisible() bool {
	if c.completedCommandMode() != "snapshot" {
		return false
	}
	switch c.chatboxConfig.SnapshotMetadata {
	case "never":
		return false
	case "changed":
		return !c.hasLastCommandSnapshot || !statusSnapshotMetadataEqual(c.lastCommandSnapshot, c.commandStatusSnapshot)
	default:
		return true
	}
}

func (c *terminalCompositor) completedCommandMode() string {
	if c.chatboxConfig.CompletedCommand == "" {
		return "snapshot"
	}
	return c.chatboxConfig.CompletedCommand
}

func statusSnapshotMetadataEqual(left, right statusSnapshot) bool {
	return left.Directory == right.Directory &&
		left.RepositoryRoot == right.RepositoryRoot &&
		left.DirectoryReadOnly == right.DirectoryReadOnly &&
		left.Git == right.Git &&
		left.Package == right.Package &&
		left.Shell == right.Shell &&
		left.Session == right.Session &&
		left.Contexts == right.Contexts &&
		maps.Equal(left.Versions, right.Versions) &&
		maps.Equal(left.ActiveVersions, right.ActiveVersions) &&
		slices.Equal(left.Environment, right.Environment) &&
		slices.Equal(left.VersionMismatches, right.VersionMismatches) &&
		slices.Equal(left.StaleProviders, right.StaleProviders)
}

func (c *terminalCompositor) writeDirectCommandOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	c.writeTerminal(data)
	c.commandOutputStarted = true
	if newline := bytes.LastIndexByte(data, '\n'); newline >= 0 {
		c.commandOutputDirect = newline < len(data)-1
		c.commandOutputLineOpen = c.commandOutputDirect
		return
	}
	c.commandOutputDirect = true
	c.commandOutputLineOpen = true
}

func (c *terminalCompositor) writePaddedCommandOutput(data []byte) {
	if len(data) > 0 {
		c.commandOutputStarted = true
	}
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			c.writePaddedCommandOutputSegment(data)
			return
		}
		segment := bytes.TrimSuffix(data[:newline], []byte{'\r'})
		c.writePaddedCommandOutputSegment(segment)
		c.writeTerminal([]byte("\r\n"))
		c.commandOutputLineOpen = false
		c.commandOutputColumn = 0
		data = data[newline+1:]
	}
}

func (c *terminalCompositor) writePaddedCommandOutputSegment(segment []byte) {
	if !c.commandOutputLineOpen {
		c.writeTerminal([]byte(strings.Repeat(" ", terminalInputHorizontalPadding)))
		c.commandOutputLineOpen = true
		c.commandOutputColumn = 0
	}
	text := string(segment)
	inner := max(c.width-2*terminalInputHorizontalPadding, 1)
	for text != "" {
		width := ansi.StringWidth(text)
		remaining := inner - c.commandOutputColumn
		if width <= remaining {
			c.writeTerminal([]byte(text))
			c.commandOutputColumn += width
			return
		}
		if remaining <= 0 {
			c.writeTerminal([]byte("\r\n" + strings.Repeat(" ", terminalInputHorizontalPadding)))
			c.commandOutputColumn = 0
			remaining = inner
		}
		piece := ansi.Cut(text, 0, remaining)
		c.writeTerminal([]byte(piece))
		c.writeTerminal([]byte("\r\n" + strings.Repeat(" ", terminalInputHorizontalPadding)))
		text = ansi.Cut(text, remaining, width)
		c.commandOutputColumn = 0
	}
}

func (c *terminalCompositor) clearAndDisableTransientUI(frame *strings.Builder) {
	if c.disableTransientUI != nil {
		frame.Write(c.disableTransientUI())
	} else if c.clearTransientUI != nil {
		frame.Write(c.clearTransientUI())
	}
	if c.setTransientUIGeometry != nil {
		c.setTransientUIGeometry(false, 0)
	}
}

func (c *terminalCompositor) updateSurfaceBottom() {
	cursorAbs := c.absoluteCursorLine()
	if cursorAbs > c.surfaceBottomAbs {
		c.surfaceBottomAbs = cursorAbs
	}
}

func (c *terminalCompositor) absoluteCursorLine() int {
	return c.emulator.ScrollbackLen() + c.emulator.CursorPosition().Y
}

func (c *terminalCompositor) renderPinned() {
	if c.height <= 1 || c.width <= 0 {
		return
	}

	cursor := c.emulator.CursorPosition()
	cursorAbs := c.absoluteCursorLine()
	c.updateSurfaceBottom()

	decorationRows := c.inputBoxDecorationRows()
	contentRows := c.surfaceBottomAbs - c.promptStartAbs + 1
	contentRows = clamp(contentRows, 1, c.height-1-decorationRows)
	sourceStartAbs := c.surfaceBottomAbs - contentRows + 1
	rows := contentRows + decorationRows
	targetTop := c.height - rows
	outputRows := targetTop
	contentLines := make([]string, contentRows)
	contentCells := make([]uv.Line, contentRows)
	scrollback := c.emulator.ScrollbackLen()
	contentWidth := c.width
	surfaceColor := ansi.XParseColor(c.inputBoxTheme.SurfaceBackground)
	promptColor := ansi.XParseColor(c.inputBoxTheme.Accent)
	promptWidth := ansi.StringWidth(strings.TrimSpace(c.chatboxConfig.Prompt))
	if surfaceColor == nil {
		surfaceColor = ansi.XParseColor(c.inputBoxTheme.Background)
	}
	if decorationRows > 0 {
		contentWidth -= 2 * terminalInputHorizontalPadding
	}
	for offset := range contentRows {
		sourceAbs := sourceStartAbs + offset
		line := uv.NewLine(contentWidth)
		for x := 0; x < contentWidth; x++ {
			if cell := c.cellAtAbsolute(x, sourceAbs, scrollback); cell != nil {
				if cell.Width == 0 {
					continue
				}
				normalized := *cell
				normalized.Content = norm.NFC.String(normalized.Content)
				if decorationRows > 0 && surfaceColor != nil {
					normalized.Style.Bg = surfaceColor
					normalized.Style.Attrs &^= uv.AttrReverse
				}
				if sourceAbs == c.promptStartAbs && x < promptWidth && promptColor != nil {
					normalized.Style.Fg = promptColor
				}
				line.Set(x, &normalized)
			}
		}
		contentCells[offset] = line
		contentLines[offset] = line.Render()
	}
	lines := c.inputBoxLines(contentLines)

	var frame strings.Builder
	frame.Grow(rows * (c.width + 24))
	frame.WriteString(terminalSyncStart)
	reflowTransientUI := c.transientGeometryDirty || c.surfaceRows != rows
	oldTop, oldRows := c.currentTransientUIRegion()
	if reflowTransientUI && c.clearTransientUI != nil {
		frame.Write(c.clearTransientUI())
	}
	if reflowTransientUI {
		frame.Write(c.restoreBackdropRows(oldTop, oldRows))
	}
	baseStart := frame.Len()
	frame.WriteString(c.promptPrelude)
	frame.WriteString("\x1b[?25l\x1b[?7l")
	if c.surfaceRows > 0 && targetTop < c.renderedTop {
		growth := c.renderedTop - targetTop
		fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[%d;1H", c.renderedTop, c.renderedTop)
		frame.WriteString(strings.Repeat("\r\n", growth))
	}
	fmt.Fprintf(&frame, "\x1b[1;%dr", outputRows)

	layoutChanged := c.surfaceRows != rows || c.renderedTop != targetTop || len(c.renderedLines) != len(lines)
	if layoutChanged && c.surfaceRows > 0 {
		oldTop := clamp(c.renderedTop, 0, c.height)
		oldBottom := clamp(c.renderedTop+c.surfaceRows, oldTop, c.height)
		newBottom := targetTop + rows
		for row := oldTop; row < oldBottom; row++ {
			if row < targetTop || row >= newBottom {
				fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row+1)
			}
		}
	}
	for offset, line := range lines {
		if !layoutChanged && offset < len(c.renderedLines) && line == c.renderedLines[offset] {
			continue
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H", targetTop+offset+1)
		if decorationRows > 0 && offset > 0 && offset <= contentRows {
			frame.WriteString(c.inputBoxBackground())
		}
		frame.WriteString("\x1b[2K")
		frame.WriteString(line)
		frame.WriteString("\x1b[0m")
	}

	contentTop := targetTop + c.inputBoxTopRows()
	targetRow := contentTop + cursorAbs - sourceStartAbs
	targetRow = clamp(targetRow, contentTop, contentTop+contentRows-1)
	cursorColumn := cursor.X + 1
	if decorationRows > 0 {
		cursorColumn += terminalInputHorizontalPadding
	}
	fmt.Fprintf(&frame, "\x1b[%d;%dH", targetRow+1, clamp(cursorColumn, 1, c.width-1))
	frame.WriteString("\x1b[?7h\x1b[?25h")
	baseEnd := frame.Len()
	if reflowTransientUI {
		if c.setTransientUIGeometry != nil {
			c.setTransientUIGeometry(true, rows)
		}
		if c.renderTransientUI != nil {
			frame.Write(c.renderTransientUI())
		}
	}
	frame.WriteString(terminalSyncEnd)
	contents := frame.String()
	_, _ = io.WriteString(c.out, contents)
	c.recordBackdrop([]byte(contents[baseStart:baseEnd]))
	c.surfaceRows = rows
	c.surfaceContentRows = contentRows
	c.surfaceContentLines = contentLines
	c.surfaceContentCells = contentCells
	c.renderedTop = targetTop
	c.renderedLines = lines
	c.transientGeometryDirty = false
	c.promptPrelude = ""
}

func (c *terminalCompositor) inputBoxDecorationRows() int {
	if c.inputBoxTheme.SurfaceBackground == "" || c.width < 12 || c.height < 4 {
		return 0
	}
	rows := 2
	if c.inputBoxStatusEnabled() {
		rows += len(c.inputBoxStatusLines())
	}
	if c.inputBoxTitleEnabled() {
		rows++
	}
	return rows
}

func (c *terminalCompositor) inputBoxTopRows() int {
	if c.inputBoxDecorationRows() == 0 {
		return 0
	}
	rows := 1
	if c.inputBoxTitleEnabled() {
		rows++
	}
	return rows
}

func (c *terminalCompositor) inputBoxLines(content []string) []string {
	if c.inputBoxDecorationRows() == 0 {
		return content
	}
	lines := make([]string, 0, len(content)+c.inputBoxDecorationRows())
	if c.inputBoxTitleEnabled() {
		lines = append(lines, c.inputBoxTitleLine())
	}
	lines = append(lines, c.inputBoxPaddingLine())
	for _, line := range content {
		lines = append(lines, c.inputBoxContent(line))
	}
	lines = append(lines, c.inputBoxPaddingLine())
	if c.inputBoxStatusEnabled() {
		lines = append(lines, c.inputBoxStatusLines()...)
	}
	return lines
}

func (c *terminalCompositor) inputBoxTitleEnabled() bool {
	if !c.chatboxConfigured || !chatboxBarConfigured(c.chatboxConfig.Title) {
		return false
	}
	minimumHeight := 5
	if chatboxBarConfigured(c.chatboxConfig.Status) {
		minimumHeight++
	}
	return c.height >= minimumHeight
}

func (c *terminalCompositor) inputBoxStatusEnabled() bool {
	return c.inputBoxStatusRowLimit() > 0
}

func (c *terminalCompositor) inputBoxStatusRowLimit() int {
	if !c.chatboxConfigured || !chatboxBarConfigured(c.chatboxConfig.Status) || c.height < 6 {
		return 0
	}
	limit := 2
	if c.chatboxConfig.Density == "compact" {
		limit = 1
	} else if c.chatboxConfig.Density == "rich" {
		limit = 3
	}
	if !c.chatboxConfig.Responsive {
		limit = max(limit, 3)
	}
	return min(limit, max(c.height-5, 1))
}

func chatboxBarConfigured(bar terminalChatboxBarConfig) bool {
	return len(bar.Left)+len(bar.Center)+len(bar.Right) > 0
}

func (c *terminalCompositor) inputBoxContent(content string) string {
	surface := c.inputBoxSurfaceCode
	if surface == "" {
		surface = c.inputBoxBackgroundCode
	}
	return c.inputBoxContentWithSurface(content, surface)
}

func (c *terminalCompositor) completedInputBoxContent(content string) string {
	surface := c.completedSurfaceCode
	if surface == "" {
		surface = c.inputBoxSurfaceCode
	}
	if surface == "" {
		surface = c.inputBoxBackgroundCode
	}
	return c.inputBoxSparseContentWithSurface(content, surface)
}

func (c *terminalCompositor) completedSurfaceContentLine(line uv.Line) string {
	surfaceColor := ansi.XParseColor(c.inputBoxTheme.CompletedSurfaceBackground)
	if surfaceColor == nil {
		surfaceColor = ansi.XParseColor(c.inputBoxTheme.SurfaceBackground)
	}
	if surfaceColor == nil {
		surfaceColor = ansi.XParseColor(c.inputBoxTheme.Background)
	}
	if surfaceColor == nil {
		return line.Render()
	}
	completed := slices.Clone(line)
	for index := range completed {
		cell := &completed[index]
		if cell.IsZero() || cell.Equal(&uv.EmptyCell) {
			continue
		}
		cell.Style.Bg = surfaceColor
	}
	return completed.Render()
}

func (c *terminalCompositor) inputBoxContentWithSurface(content, surface string) string {
	inner := c.width - 2*terminalInputHorizontalPadding
	content = norm.NFC.String(content)
	if ansi.StringWidth(content) > inner {
		content = ansi.Cut(content, 0, inner)
	}
	padding := max(inner-ansi.StringWidth(content), 0)
	content = c.replaceInputBoxSurface(content, surface)
	horizontalPadding := strings.Repeat(" ", terminalInputHorizontalPadding)
	return surface + horizontalPadding + content + strings.Repeat(" ", padding) + horizontalPadding
}

func (c *terminalCompositor) inputBoxSparseContentWithSurface(content, surface string) string {
	inner := c.width - 2*terminalInputHorizontalPadding
	content = norm.NFC.String(content)
	if ansi.StringWidth(content) > inner {
		content = ansi.Cut(content, 0, inner)
	}
	content = c.replaceInputBoxSurface(content, surface)
	return fmt.Sprintf("%s\x1b[2K\x1b[%dG%s", surface, terminalInputHorizontalPadding+1, content)
}

func (c *terminalCompositor) replaceInputBoxSurface(content, surface string) string {
	if c.inputBoxSurfaceCode != "" && c.inputBoxSurfaceCode != surface {
		content = strings.ReplaceAll(content, c.inputBoxSurfaceCode, surface)
	}
	content = strings.ReplaceAll(content, ansi.ResetStyle, ansi.ResetStyle+surface)
	content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+surface)
	content = strings.ReplaceAll(content, "\x1b[49m", "\x1b[49m"+surface)
	return content
}

func (c *terminalCompositor) inputBoxPaddingLine() string {
	surface := c.inputBoxSurfaceCode
	if surface == "" {
		surface = c.inputBoxBackgroundCode
	}
	return surface + strings.Repeat(" ", c.width)
}

func (c *terminalCompositor) completedInputBoxPaddingLine() string {
	surface := c.completedSurfaceCode
	if surface == "" {
		surface = c.inputBoxSurfaceCode
	}
	if surface == "" {
		surface = c.inputBoxBackgroundCode
	}
	return surface + "\x1b[2K"
}

func (c *terminalCompositor) completedExecutionHeaderLine() string {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	startedAt := c.commandStartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}
	snapshot := cloneStatusSnapshot(c.commandStatusSnapshot)
	if snapshot.Directory == "" {
		snapshot.Directory = c.commandDirectory
	}
	segments := c.inputBoxBarSegmentsForSnapshot(c.chatboxConfig.Title, snapshot)
	segments = append(segments, terminalStatusSegment{
		name: "execution-time", text: startedAt.Format("15:04"), priority: 100, alignment: terminalStatusRight,
	})
	return c.renderBarLine(segments)
}

func (c *terminalCompositor) completedExecutionContextLines() []string {
	bar := filterChatboxBar(c.chatboxConfig.Status, func(name string) bool {
		return !isCommandOutcomeStatus(name)
	})
	if !chatboxBarConfigured(bar) {
		return nil
	}
	rowLimit := max(c.inputBoxStatusRowLimit(), 1)
	segments := c.inputBoxBarSegmentsForSnapshot(bar, c.commandStatusSnapshot)
	return c.renderBarLines(segments, rowLimit)
}

func (c *terminalCompositor) completedExecutionOutcomeLines(duration time.Duration, exitCode int) []string {
	bar := filterChatboxBar(c.chatboxConfig.Status, isCommandOutcomeStatus)
	if !chatboxBarConfigured(bar) {
		return nil
	}
	snapshot := cloneStatusSnapshot(c.commandStatusSnapshot)
	snapshot.Duration = duration
	snapshot.ExitCode = &exitCode
	rowLimit := max(c.inputBoxStatusRowLimit(), 1)
	segments := c.inputBoxBarSegmentsForSnapshot(bar, snapshot)
	return c.renderBarLinesWithBackground(segments, rowLimit, c.completedMetadataBackground())
}

func filterChatboxBar(
	bar terminalChatboxBarConfig,
	keep func(string) bool,
) terminalChatboxBarConfig {
	filter := func(names []string) []string {
		filtered := make([]string, 0, len(names))
		for _, name := range names {
			if keep(name) {
				filtered = append(filtered, name)
			}
		}
		return filtered
	}
	return terminalChatboxBarConfig{
		Left:   filter(bar.Left),
		Center: filter(bar.Center),
		Right:  filter(bar.Right),
	}
}

func isCommandOutcomeStatus(name string) bool {
	return name == "duration" || name == "exit"
}

func (c *terminalCompositor) completedMetadataBackground() string {
	if c.completedSurfaceCode != "" {
		return c.completedSurfaceCode
	}
	if c.inputBoxSurfaceCode != "" {
		return c.inputBoxSurfaceCode
	}
	return c.inputBoxBackgroundCode
}

func (c *terminalCompositor) inputBoxBarSegments(bar terminalChatboxBarConfig) []terminalStatusSegment {
	return c.inputBoxBarSegmentsForSnapshot(bar, c.statusSnapshot)
}

func (c *terminalCompositor) inputBoxBarSegmentsForSnapshot(
	bar terminalChatboxBarConfig,
	snapshot statusSnapshot,
) []terminalStatusSegment {
	segments := make([]terminalStatusSegment, 0, len(bar.Left)+len(bar.Center)+len(bar.Right)+8)
	for _, region := range []struct {
		names     []string
		alignment terminalStatusAlignment
	}{
		{bar.Left, terminalStatusLeft},
		{bar.Center, terminalStatusCenter},
		{bar.Right, terminalStatusRight},
	} {
		segments = append(segments, c.inputBoxSegmentsForSnapshot(region.names, region.alignment, snapshot)...)
	}
	return segments
}

func (c *terminalCompositor) inputBoxSegmentsForSnapshot(
	names []string,
	alignment terminalStatusAlignment,
	snapshot statusSnapshot,
) []terminalStatusSegment {
	segments := make([]terminalStatusSegment, 0, len(names)+8)
	add := func(name, text string, priority int) {
		if text != "" {
			segments = append(segments, terminalStatusSegment{name: name, text: text, priority: priority, alignment: alignment})
		}
	}
	if snapshot.Directory == "" {
		snapshot.Directory = c.inputBoxPath
	}
	if snapshot.ExitCode == nil {
		snapshot.ExitCode = c.lastExitStatus
	}
	for _, name := range names {
		switch name {
		case "directory":
			add(name, c.repositoryAwarePath(snapshot), 90)
			if snapshot.DirectoryReadOnly {
				add("directory-read-only", "read-only", 95)
			}
		case "package":
			value := strings.TrimSpace(strings.Join([]string{snapshot.Package.Name, snapshot.Package.Version}, " "))
			add(name, value, 35)
		case "session":
			if snapshot.Session.SSH {
				add(name, "ssh "+strings.Trim(strings.Join([]string{snapshot.Session.User, snapshot.Session.Host}, "@"), "@"), 100)
			}
			if snapshot.Session.Container != "" {
				add(name, "container "+snapshot.Session.Container, 100)
			}
			if snapshot.Session.Root {
				add("session-critical", "root", 100)
			}
			if snapshot.Session.Sudo {
				add("session-warning", "sudo", 95)
			}
		case "git-branch":
			add(name, snapshot.Git.Branch, 80)
		case "git-status":
			gitSegments := gitStatusSegments(snapshot.Git)
			for index := range gitSegments {
				gitSegments[index].alignment = alignment
			}
			segments = append(segments, gitSegments...)
		case "git-added":
			if snapshot.Git.Added > 0 {
				add(name, fmt.Sprintf("added %d", snapshot.Git.Added), 60)
			}
		case "git-deleted":
			if snapshot.Git.Deleted > 0 {
				add(name, fmt.Sprintf("deleted %d", snapshot.Git.Deleted), 60)
			}
		case "git-stash":
			if snapshot.Git.StashCount > 0 {
				add(name, fmt.Sprintf("stash %d", snapshot.Git.StashCount), 45)
			}
		case "git-lines":
			if snapshot.Git.LinesAdded > 0 {
				add("git-lines-added", fmt.Sprintf("+%d", snapshot.Git.LinesAdded), 35)
			}
			if snapshot.Git.LinesDeleted > 0 {
				add("git-lines-deleted", fmt.Sprintf("-%d", snapshot.Git.LinesDeleted), 35)
			}
		case "environment":
			for _, environment := range snapshot.Environment {
				add(name, environment, 45)
			}
		case "version-mismatch":
			for _, mismatch := range snapshot.VersionMismatches {
				add(name, fmt.Sprintf("%s %s≠%s", statusToolDisplayName(mismatch.Tool), mismatch.Declared, mismatch.Active), 95)
			}
		case "contexts":
			commandContext := snapshot.CommandContext
			if commandContext == "" {
				commandContext = c.commandContext
			}
			for _, context := range c.visibleContexts(commandContext, snapshot.Contexts) {
				add(name, context, 85)
			}
		case "stale":
			add(name, staleStatusText(snapshot.StaleProviders), 90)
		case "jobs":
			if snapshot.Shell.Jobs > 0 {
				add(name, fmt.Sprintf("jobs %d", snapshot.Shell.Jobs), 55)
			}
			if snapshot.Shell.StoppedJobs > 0 {
				add("jobs-stopped", fmt.Sprintf("stopped %d", snapshot.Shell.StoppedJobs), 90)
			}
		case "duration":
			if snapshot.ExitCode != nil {
				add(c.durationStatusColor(snapshot.Duration), formatStatusDuration(snapshot.Duration), 70)
			}
		case "exit":
			if snapshot.ExitCode == nil {
				add("exit-neutral", "exit —", 1)
				continue
			}
			priority := 1
			if *snapshot.ExitCode != 0 {
				priority = 100
			}
			text := fmt.Sprintf("exit %d", *snapshot.ExitCode)
			if meaning := semanticExitStatus(*snapshot.ExitCode); meaning != "" {
				text += " " + meaning
			}
			add(exitStatusColor(*snapshot.ExitCode), text, priority)
		case "versions":
			if c.chatboxConfig.Versions == "never" {
				continue
			}
			for _, tool := range statusVersionOrder {
				if c.versionVisible(tool) {
					if version, ok := snapshot.Versions[tool]; ok && version != "" {
						add(tool, statusToolName(tool)+" "+version, 40)
					}
				}
			}
		case "cpu":
			if c.metricVisible(snapshot.CPU, c.chatboxConfig.CPUHigh) && (snapshot.HasCPU || snapshot.CPU > 0) {
				add(c.loadStatusColor("cpu", snapshot.CPU), fmt.Sprintf("CPU %.0f%%", snapshot.CPU), 3)
			}
		case "memory":
			if c.metricVisible(snapshot.Memory, c.chatboxConfig.MemoryHigh) && (snapshot.HasMemory || snapshot.Memory > 0) {
				add(c.loadStatusColor("memory", snapshot.Memory), fmt.Sprintf("RAM %.0f%%", snapshot.Memory), 2)
			}
		}
	}
	return segments
}

func (c *terminalCompositor) inputBoxTitleLine() string {
	if c.titleLinesCache == nil {
		c.titleLinesCache = []string{c.inputBoxBarLine(c.chatboxConfig.Title)}
	}
	return c.titleLinesCache[0]
}

func (c *terminalCompositor) inputBoxStatusLine() string {
	return c.inputBoxStatusLines()[0]
}

func (c *terminalCompositor) inputBoxStatusLines() []string {
	if c.statusLinesCache == nil {
		rowLimit := max(c.inputBoxStatusRowLimit(), 1)
		c.statusLinesCache = c.renderBarLines(c.inputBoxBarSegments(c.chatboxConfig.Status), rowLimit)
	}
	return c.statusLinesCache
}

func (c *terminalCompositor) inputBoxBarLine(bar terminalChatboxBarConfig) string {
	segments := c.inputBoxBarSegments(bar)
	return c.renderBarLine(segments)
}

func (c *terminalCompositor) renderBarLine(segments []terminalStatusSegment) string {
	return c.renderBarLines(segments, 1)[0]
}

func (c *terminalCompositor) renderBarLines(segments []terminalStatusSegment, rowLimit int) []string {
	background := c.inputBoxStatusCode
	if background == "" {
		background = c.inputBoxBackgroundCode
	}
	return c.renderBarLinesWithBackground(segments, rowLimit, background)
}

func (c *terminalCompositor) renderBarLinesWithBackground(
	segments []terminalStatusSegment,
	rowLimit int,
	background string,
) []string {
	inner := c.width - 2*terminalInputHorizontalPadding
	separator := c.chatboxConfig.Separator
	if c.chatboxConfig.Density == "compact" {
		segments = slices.DeleteFunc(segments, func(segment terminalStatusSegment) bool {
			return segment.priority <= 45
		})
	}
	for index := range segments {
		if segments[index].name == "directory" && ansi.StringWidth(segments[index].text) > max(inner/2, 12) {
			segments[index].text = shortenStatusPath(segments[index].text, max(inner/2, 12))
		}
		if ansi.StringWidth(segments[index].text) > inner {
			segments[index].text = ansi.Truncate(segments[index].text, inner, "…")
		}
	}
	rows := layoutStatusRows(segments, inner, separator, 1)
	if rows == nil && rowLimit > 1 {
		if c.chatboxConfig.Responsive && c.chatboxConfig.Density != "rich" {
			segments = slices.DeleteFunc(segments, func(segment terminalStatusSegment) bool {
				return segment.priority <= 3 || (c.chatboxConfig.CollapseVersions && segment.priority == 40)
			})
		}
		rows = layoutStatusRows(segments, inner, separator, rowLimit)
	}
	for rows == nil && len(segments) > 0 {
		remove := lowestPriorityStatusSegment(segments)
		if c.chatboxConfig.CollapseVersions && segments[remove].priority == 40 {
			segments = slices.DeleteFunc(segments, func(value terminalStatusSegment) bool { return value.priority == 40 })
		} else {
			segments = append(segments[:remove], segments[remove+1:]...)
		}
		rows = layoutStatusRows(segments, inner, separator, rowLimit)
	}
	if rows == nil {
		rows = [][]terminalStatusSegment{{}}
	}
	rendered := make([]string, len(rows))
	for index, row := range rows {
		rendered[index] = c.renderBarRow(row, inner, separator, background)
	}
	return rendered
}

func (c *terminalCompositor) renderBarRow(
	segments []terminalStatusSegment,
	inner int,
	separator string,
	background string,
) string {
	left, center, right := splitStatusSegments(segments)
	if background == "" {
		background = c.inputBoxBackgroundCode
	}
	leftWidth := statusSegmentWidth(left, separator)
	centerWidth := statusSegmentWidth(center, separator)
	rightWidth := statusSegmentWidth(right, separator)
	centerStart := max((inner-centerWidth)/2, leftWidth)
	rightStart := inner - rightWidth
	var line strings.Builder
	padding := strings.Repeat(" ", terminalInputHorizontalPadding)
	line.WriteString(background)
	line.WriteString(padding)
	column := 0
	writeGroup := func(start int, group []terminalStatusSegment, width int) {
		if len(group) == 0 {
			return
		}
		line.WriteString(background)
		line.WriteString(strings.Repeat(" ", max(start-column, 0)))
		line.WriteString(c.inputBoxStatusTextCode)
		line.WriteString(c.renderStatusSegments(group))
		column = start + width
	}
	writeGroup(0, left, leftWidth)
	writeGroup(centerStart, center, centerWidth)
	writeGroup(rightStart, right, rightWidth)
	line.WriteString(background)
	line.WriteString(strings.Repeat(" ", max(inner-column, 0)))
	line.WriteString(padding)
	return line.String()
}

func layoutStatusRows(segments []terminalStatusSegment, width int, separator string, rowLimit int) [][]terminalStatusSegment {
	if statusSegmentsFit(segments, width, separator) {
		return [][]terminalStatusSegment{segments}
	}
	if rowLimit < 2 || len(segments) < 2 {
		return nil
	}
	bestSplit := -1
	bestBalance := int(^uint(0) >> 1)
	for split := 1; split < len(segments); split++ {
		first, second := segments[:split], segments[split:]
		if !statusSegmentsFit(first, width, separator) || !statusSegmentsFit(second, width, separator) {
			continue
		}
		balance := absoluteInt(statusSegmentsContentWidth(first, separator) - statusSegmentsContentWidth(second, separator))
		if balance < bestBalance {
			bestSplit = split
			bestBalance = balance
		}
	}
	if bestSplit < 0 {
		return nil
	}
	return [][]terminalStatusSegment{segments[:bestSplit], segments[bestSplit:]}
}

func statusSegmentsContentWidth(segments []terminalStatusSegment, separator string) int {
	left, center, right := splitStatusSegments(segments)
	return statusSegmentWidth(left, separator) + statusSegmentWidth(center, separator) + statusSegmentWidth(right, separator)
}

func absoluteInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func lowestPriorityStatusSegment(segments []terminalStatusSegment) int {
	if len(segments) == 0 {
		return -1
	}
	lowest := 0
	for index := 1; index < len(segments); index++ {
		if segments[index].priority < segments[lowest].priority {
			lowest = index
		}
	}
	return lowest
}

func splitStatusSegments(segments []terminalStatusSegment) ([]terminalStatusSegment, []terminalStatusSegment, []terminalStatusSegment) {
	left := make([]terminalStatusSegment, 0, len(segments))
	center := make([]terminalStatusSegment, 0, len(segments))
	right := make([]terminalStatusSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.alignment {
		case terminalStatusCenter:
			center = append(center, segment)
		case terminalStatusRight:
			right = append(right, segment)
		default:
			left = append(left, segment)
		}
	}
	return left, center, right
}

func statusSegmentsFit(segments []terminalStatusSegment, width int, separator string) bool {
	left, center, right := splitStatusSegments(segments)
	leftWidth := statusSegmentWidth(left, separator)
	centerWidth := statusSegmentWidth(center, separator)
	rightWidth := statusSegmentWidth(right, separator)
	if leftWidth > width || centerWidth > width || rightWidth > width {
		return false
	}
	centerStart := (width - centerWidth) / 2
	rightStart := width - rightWidth
	if len(center) > 0 {
		if len(left) > 0 && leftWidth >= centerStart {
			return false
		}
		if len(right) > 0 && centerStart+centerWidth >= rightStart {
			return false
		}
		return true
	}
	return len(left) == 0 || len(right) == 0 || leftWidth < rightStart
}

func statusSegmentWidth(segments []terminalStatusSegment, separator string) int {
	width := 0
	for index, segment := range segments {
		if index > 0 {
			width += ansi.StringWidth(separator)
		}
		width += ansi.StringWidth(segment.text)
	}
	return width
}

func (c *terminalCompositor) renderStatusSegments(segments []terminalStatusSegment) string {
	var content strings.Builder
	for index, segment := range segments {
		if index > 0 {
			content.WriteString(c.inputBoxStatusTextCode)
			content.WriteString(c.chatboxConfig.Separator)
		}
		content.WriteString(c.statusColorCode(segment.name))
		content.WriteString(segment.text)
	}
	return content.String()
}

func (c *terminalCompositor) statusColorCode(name string) string {
	if color := c.chatboxColorCodes[name]; color != "" {
		return color
	}
	switch {
	case name == "execution-time":
		return terminalTrueColor("38", c.inputBoxTheme.Muted)
	case strings.HasPrefix(name, "git-"):
		return c.chatboxColorCodes["git-status"]
	case strings.HasPrefix(name, "duration-"):
		return c.chatboxColorCodes["duration"]
	case strings.HasPrefix(name, "cpu-") || strings.HasPrefix(name, "memory-"):
		_, level, _ := strings.Cut(name, "-")
		return c.chatboxColorCodes["load-"+level]
	default:
		return c.inputBoxStatusTextCode
	}
}

func (c *terminalCompositor) repositoryAwarePath(snapshot statusSnapshot) string {
	directory := displayTerminalPath(snapshot.Directory)
	root := displayTerminalPath(snapshot.RepositoryRoot)
	segments := limitedDisplayPathSegments(directory, root, c.chatboxConfig.PathMaxSegments)
	path := joinDisplayPathSegments(segments)
	if c.chatboxConfig.PathColorMode != "hierarchy" || len(segments) < 2 {
		return c.statusColorCode("directory") + path
	}

	start := c.chatboxConfig.Colors["directory-root"]
	end := c.chatboxConfig.Colors["directory"]
	if start == "" || end == "" {
		return c.statusColorCode("directory") + path
	}

	var rendered strings.Builder
	colorIndex := 0
	colorCount := len(segments)
	if segments[0] == "/" {
		colorCount--
	}
	for index, segment := range segments {
		if segment == "/" {
			rendered.WriteString("/")
			continue
		}
		if index > 0 && segments[index-1] != "/" {
			rendered.WriteByte('/')
		}
		rendered.WriteString(terminalTrueColor("38", interpolateHexColor(start, end, colorIndex, max(colorCount-1, 1))))
		rendered.WriteString(segment)
		colorIndex++
	}
	return rendered.String()
}

func limitedDisplayPathSegments(path, root string, maximum int) []string {
	segments := splitDisplayPath(path)
	if maximum < 3 || len(segments) < maximum {
		return segments
	}
	rootSegments := splitDisplayPath(root)
	anchor := commonPathSegmentPrefix(segments, rootSegments)
	if anchor == 0 {
		anchor = 1
	}
	if anchor == len(segments) {
		if len(segments) == maximum {
			return append([]string{segments[0], "…"}, segments[2:]...)
		}
		return compactPathAnchor(segments, maximum)
	}
	anchorSegments := compactPathAnchor(segments[:anchor], maximum-2)
	tail := maximum - len(anchorSegments) - 1
	return append(append(append([]string(nil), anchorSegments...), "…"), segments[len(segments)-tail:]...)
}

func compactPathAnchor(segments []string, maximum int) []string {
	if len(segments) <= maximum {
		return append([]string(nil), segments...)
	}
	if maximum == 1 {
		return []string{segments[len(segments)-1]}
	}
	if maximum == 2 {
		return []string{segments[0], segments[len(segments)-1]}
	}
	tail := maximum - 2
	return append([]string{segments[0], "…"}, segments[len(segments)-tail:]...)
}

func splitDisplayPath(path string) []string {
	path = filepath.ToSlash(filepath.Clean(path))
	switch {
	case path == "/":
		return []string{"/"}
	case strings.HasPrefix(path, "/"):
		return append([]string{"/"}, strings.Split(strings.TrimPrefix(path, "/"), "/")...)
	default:
		return strings.Split(path, "/")
	}
}

func joinDisplayPathSegments(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	if segments[0] == "/" {
		if len(segments) == 1 {
			return "/"
		}
		return "/" + strings.Join(segments[1:], "/")
	}
	return strings.Join(segments, "/")
}

func commonPathSegmentPrefix(left, right []string) int {
	count := 0
	for count < len(left) && count < len(right) && left[count] == right[count] {
		count++
	}
	return count
}

func interpolateHexColor(start, end string, numerator, denominator int) string {
	parse := func(value string) ([3]uint64, bool) {
		value = strings.TrimPrefix(value, "#")
		if len(value) != 6 {
			return [3]uint64{}, false
		}
		var channels [3]uint64
		for index := range channels {
			channel, err := strconv.ParseUint(value[index*2:index*2+2], 16, 8)
			if err != nil {
				return [3]uint64{}, false
			}
			channels[index] = channel
		}
		return channels, true
	}
	from, fromOK := parse(start)
	to, toOK := parse(end)
	if !fromOK || !toOK || denominator <= 0 {
		return end
	}
	var result [3]uint64
	for index := range result {
		result[index] = (from[index]*uint64(denominator-numerator) + to[index]*uint64(numerator)) / uint64(denominator)
	}
	return fmt.Sprintf("#%02x%02x%02x", result[0], result[1], result[2])
}

func shortenStatusPath(path string, width int) string {
	if ansi.StringWidth(path) <= width {
		return path
	}
	plain := ansi.Strip(path)
	base := filepath.Base(plain)
	totalWidth := ansi.StringWidth(path)
	baseWidth := ansi.StringWidth(base)
	coloredBase := ansi.Cut(path, max(totalWidth-baseWidth, 0), totalWidth)
	short := "…/" + coloredBase
	if ansi.StringWidth(short) <= width {
		return short
	}
	return ansi.Truncate(coloredBase, width, "…")
}

func gitStatusSegments(status gitStatusSnapshot) []terminalStatusSegment {
	segments := make([]terminalStatusSegment, 0, 8)
	add := func(name, text string, priority int) {
		if text != "" {
			segments = append(segments, terminalStatusSegment{name: name, text: text, priority: priority})
		}
	}
	add("git-operation", status.Operation, 85)
	if status.Conflicts > 0 {
		add("git-conflicts", fmt.Sprintf("conflicts %d", status.Conflicts), 85)
	}
	if status.Staged > 0 {
		add("git-staged", fmt.Sprintf("staged %d", status.Staged), 60)
	}
	if status.Modified > 0 {
		add("git-modified", fmt.Sprintf("modified %d", status.Modified), 60)
	}
	if status.Renamed > 0 {
		add("git-renamed", fmt.Sprintf("renamed %d", status.Renamed), 60)
	}
	if status.Untracked > 0 {
		add("git-untracked", fmt.Sprintf("untracked %d", status.Untracked), 60)
	}
	if status.Ahead > 0 {
		add("git-ahead", fmt.Sprintf("ahead %d", status.Ahead), 60)
	}
	if status.Behind > 0 {
		add("git-behind", fmt.Sprintf("behind %d", status.Behind), 60)
	}
	if len(segments) == 0 && status.Resolved && status.Branch != "" {
		add("git-clean", "clean", 50)
	}
	return segments
}

func durationStatusColor(duration time.Duration) string {
	switch {
	case duration < 500*time.Millisecond:
		return "duration-fast"
	case duration < 2*time.Second:
		return "duration-average"
	default:
		return "duration-slow"
	}
}

func (c *terminalCompositor) durationStatusColor(duration time.Duration) string {
	fast, slow := c.chatboxConfig.DurationFast, c.chatboxConfig.DurationSlow
	if fast <= 0 {
		fast = 500 * time.Millisecond
	}
	if slow <= fast {
		slow = 2 * time.Second
	}
	switch {
	case duration < fast:
		return "duration-fast"
	case duration < slow:
		return "duration-average"
	default:
		return "duration-slow"
	}
}

func loadStatusColor(metric string, value float64) string {
	prefix := metric + "-"
	switch {
	case value < 50:
		return prefix + "low"
	case value < 75:
		return prefix + "average"
	case value < 90:
		return prefix + "high"
	default:
		return prefix + "critical"
	}
}

func (c *terminalCompositor) loadStatusColor(metric string, value float64) string {
	average, high, critical := c.chatboxConfig.CPUAverage, c.chatboxConfig.CPUHigh, c.chatboxConfig.CPUCritical
	if metric == "memory" {
		average, high, critical = c.chatboxConfig.MemoryAverage, c.chatboxConfig.MemoryHigh, c.chatboxConfig.MemoryCritical
	}
	if average <= 0 || high <= average || critical <= high {
		average, high, critical = 50, 75, 90
	}
	prefix := metric + "-"
	switch {
	case value < float64(average):
		return prefix + "low"
	case value < float64(high):
		return prefix + "average"
	case value < float64(critical):
		return prefix + "high"
	default:
		return prefix + "critical"
	}
}

func (c *terminalCompositor) metricVisible(value float64, high int) bool {
	switch c.chatboxConfig.Metrics {
	case "never":
		return false
	case "when-high":
		return value >= float64(high)
	default:
		return true
	}
}

func (c *terminalCompositor) versionVisible(provider string) bool {
	if slices.Contains(c.chatboxConfig.VersionDeny, provider) {
		return false
	}
	return len(c.chatboxConfig.VersionAllow) == 0 || slices.Contains(c.chatboxConfig.VersionAllow, provider)
}

func (c *terminalCompositor) visibleContexts(command string, contexts operationalContextSnapshot) []string {
	executable := statusCommandContext(command)
	result := make([]string, 0, 3)
	show := func(policy, provider string) bool {
		return policy == "always" || ((policy == "" || policy == "auto") && executable == provider)
	}
	if show(c.chatboxConfig.KubernetesContext, "kubernetes") && contexts.Kubernetes != "" {
		value := "Kube " + contexts.Kubernetes
		if contexts.KubernetesNamespace != "" {
			value += "/" + contexts.KubernetesNamespace
		}
		result = append(result, value)
	}
	if show(c.chatboxConfig.AWSContext, "aws") {
		value := strings.TrimSpace(strings.Join([]string{contexts.AWSProfile, contexts.AWSRegion}, " "))
		if value != "" {
			result = append(result, "AWS "+value)
		}
	}
	if show(c.chatboxConfig.DockerContext, "docker") && contexts.Docker != "" {
		result = append(result, "Docker "+contexts.Docker)
	}
	return result
}

func exitStatusColor(exitCode int) string {
	if exitCode == 0 {
		return "exit-success"
	}
	return "exit-failure"
}

func statusToolName(name string) string {
	switch name {
	case "php":
		return "PHP"
	case "go":
		return "Go"
	case "node":
		return "Node"
	case "bun":
		return "Bun"
	case "rust":
		return "Rust"
	case "laravel":
		return "Laravel"
	case "composer":
		return "Composer"
	case "python":
		return "Python"
	case "ruby":
		return "Ruby"
	case "elixir":
		return "Elixir"
	case "docker":
		return "Docker"
	case "docker-compose":
		return "Compose"
	default:
		return name
	}
}

func formatStatusDuration(duration time.Duration) string {
	if duration <= 0 {
		return ""
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < 10*time.Second {
		value := strconv.FormatFloat(duration.Seconds(), 'f', 2, 64)
		return strings.TrimRight(strings.TrimRight(value, "0"), ".") + "s"
	}
	return duration.Round(time.Second).String()
}

func (c *terminalCompositor) inputBoxBackground() string {
	return c.inputBoxBackgroundCode
}

func terminalTrueColor(prefix, value string) string {
	if index, err := strconv.Atoi(value); err == nil && index >= 0 && index <= 15 {
		base := 30
		if prefix == "48" {
			base = 40
		}
		if index >= 8 {
			base += 60
			index -= 8
		}
		return fmt.Sprintf("\x1b[%dm", base+index)
	}
	hex := strings.TrimPrefix(value, "#")
	if len(hex) != 6 {
		return ""
	}
	red, redErr := strconv.ParseUint(hex[0:2], 16, 8)
	green, greenErr := strconv.ParseUint(hex[2:4], 16, 8)
	blue, blueErr := strconv.ParseUint(hex[4:6], 16, 8)
	if redErr != nil || greenErr != nil || blueErr != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[%s;2;%d;%d;%dm", prefix, red, green, blue)
}

func displayTerminalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if path == home {
			return "~"
		}
		if after, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok {
			return "~/" + after
		}
	}
	return path
}

func (c *terminalCompositor) writeModel(data []byte) {
	defer func() {
		if recover() == nil {
			return
		}
		c.modelRecoveries++
		c.closeEmulator()
		c.resetEmulator(c.width, c.height)
		c.awaitingPrompt = true
		c.phase = terminalOutput
		c.promptStartAbs = 0
		c.surfaceBottomAbs = 0
		c.surfaceRows = 0
		c.surfaceContentRows = 0
		c.surfaceContentLines = nil
		c.surfaceContentCells = nil
		c.renderedLines = nil
		c.pendingLineBreak = false
		c.pendingRedraw = c.pendingRedraw[:0]
		c.promptPrelude = ""
		c.commandCardOpen = false
		c.commandOutputDirect = false
		c.commandOutputLineOpen = false
		c.commandOutputPositioned = false
		c.commandOutputStarted = false
		c.commandOutputColumn = 0
		c.commandStartedAt = time.Time{}
		c.commandDirectory = ""
		c.commandStatusSnapshot = statusSnapshot{}
		c.transientGeometryDirty = true
	}()

	data = c.sanitizeModelCSI(data)
	if len(data) == 0 {
		return
	}
	analysis := data
	if len(c.modelUTF8Tail) > 0 {
		analysis = make([]byte, 0, len(c.modelUTF8Tail)+len(data))
		analysis = append(analysis, c.modelUTF8Tail...)
		analysis = append(analysis, data...)
	}
	c.modelUTF8Tail = trailingIncompleteUTF8(analysis)
	complete := analysis[:len(analysis)-len(c.modelUTF8Tail)]
	attachment := c.attachLeadingZeroWidthRun(complete)
	_, _ = c.emulator.Write(norm.NFC.Bytes(complete))
	c.attachTrailingZeroWidthCell(attachment)
}

func (c *terminalCompositor) sanitizeModelCSI(data []byte) []byte {
	if len(c.modelCSITail) > 0 {
		combined := make([]byte, 0, len(c.modelCSITail)+len(data))
		combined = append(combined, c.modelCSITail...)
		combined = append(combined, data...)
		data = combined
		c.modelCSITail = c.modelCSITail[:0]
	}

	result := make([]byte, 0, len(data))
	for index := 0; index < len(data); {
		if data[index] != '\x1b' {
			result = append(result, data[index])
			index++
			continue
		}
		if index+1 >= len(data) {
			c.modelCSITail = append(c.modelCSITail, data[index:]...)
			break
		}
		if data[index+1] != '[' {
			result = append(result, data[index])
			index++
			continue
		}

		end := index + 2
		for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
			end++
		}
		if end >= len(data) {
			c.modelCSITail = append(c.modelCSITail, data[index:]...)
			break
		}

		sequence := data[index : end+1]
		if data[end] == 'r' {
			sequence = clampModelVerticalMargins(sequence, c.height)
		} else if data[end] == 's' && end > index+2 {
			sequence = clampModelHorizontalMargins(sequence, c.width)
		}
		result = append(result, sequence...)
		index = end + 1
	}
	return result
}

func clampModelHorizontalMargins(sequence []byte, width int) []byte {
	return clampModelMargins(sequence, width, 's')
}

func clampModelVerticalMargins(sequence []byte, height int) []byte {
	return clampModelMargins(sequence, height, 'r')
}

func clampModelMargins(sequence []byte, maximum int, final byte) []byte {
	if maximum < 2 || len(sequence) < 3 || sequence[0] != '\x1b' || sequence[1] != '[' || sequence[len(sequence)-1] != final {
		return sequence
	}

	params := strings.Split(string(sequence[2:len(sequence)-1]), ";")
	if len(params) > 2 {
		return sequence
	}
	start, end := 1, maximum
	var err error
	if len(params) > 0 && params[0] != "" {
		start, err = strconv.Atoi(params[0])
		if err != nil {
			return sequence
		}
	}
	if len(params) == 2 && params[1] != "" {
		end, err = strconv.Atoi(params[1])
		if err != nil {
			return sequence
		}
	}
	start = clamp(start, 1, maximum)
	end = clamp(end, 1, maximum)
	if start >= end {
		return []byte(fmt.Sprintf("\x1b[%c", final))
	}
	return []byte(fmt.Sprintf("\x1b[%d;%d%c", start, end, final))
}

type modelCellAttachment struct {
	content string
	cursorX int
	cursorY int
}

func (c *terminalCompositor) attachLeadingZeroWidthRun(data []byte) *modelCellAttachment {
	cursor := c.emulator.CursorPosition()
	if cursor.X <= 0 || len(data) == 0 {
		return nil
	}

	consumed := 0
	var content strings.Builder
	for consumed < len(data) {
		r, size := utf8.DecodeRune(data[consumed:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if r < utf8.RuneSelf || ansi.StringWidth(string(r)) != 0 {
			break
		}
		content.WriteRune(r)
		consumed += size
	}
	if content.Len() == 0 || consumed == len(data) || !utf8.FullRune(data[consumed:]) {
		return nil
	}

	for baseX := cursor.X - 1; baseX >= 0; baseX-- {
		base := c.emulator.CellAt(baseX, cursor.Y)
		if base == nil || base.Width <= 0 {
			continue
		}
		if baseX+base.Width != cursor.X {
			return nil
		}
		marks := content.String()
		merged := *base
		merged.Content += marks
		c.emulator.SetCell(baseX, cursor.Y, &merged)
		return &modelCellAttachment{content: marks, cursorX: cursor.X, cursorY: cursor.Y}
	}
	return nil
}

func (c *terminalCompositor) attachTrailingZeroWidthCell(attachment *modelCellAttachment) {
	cursor := c.emulator.CursorPosition()
	if cursor.X <= 0 {
		return
	}

	trailing := c.emulator.CellAt(cursor.X, cursor.Y)
	if trailing == nil || trailing.Width != 0 || trailing.Content == "" {
		return
	}
	if attachment != nil &&
		cursor.X == attachment.cursorX &&
		cursor.Y == attachment.cursorY &&
		trailing.Content == attachment.content {
		c.emulator.SetCell(cursor.X, cursor.Y, nil)
		return
	}
	for baseX := cursor.X - 1; baseX >= 0; baseX-- {
		base := c.emulator.CellAt(baseX, cursor.Y)
		if base == nil || base.Width <= 0 {
			continue
		}
		if baseX+base.Width != cursor.X {
			return
		}
		merged := *base
		merged.Content += trailing.Content
		c.emulator.SetCell(baseX, cursor.Y, &merged)
		c.emulator.SetCell(cursor.X, cursor.Y, nil)
		return
	}
}

func trailingIncompleteUTF8(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	start := len(data) - 1
	limit := max(len(data)-utf8.UTFMax, 0)
	for start >= limit && !utf8.RuneStart(data[start]) {
		start--
	}
	if start < limit || utf8.FullRune(data[start:]) {
		return nil
	}
	return append([]byte(nil), data[start:]...)
}

func requiresCommandPassThrough(data []byte) bool {
	for index := 0; index < len(data); index++ {
		if data[index] == '\r' && (index+1 >= len(data) || data[index+1] != '\n') {
			return true
		}
		if data[index] != '\x1b' || index+1 >= len(data) || data[index+1] != '[' {
			continue
		}
		for end := index + 2; end < len(data); end++ {
			if data[end] < 0x40 || data[end] > 0x7e {
				continue
			}
			if data[end] != 'm' {
				return true
			}
			index = end
			break
		}
	}
	return false
}

func (c *terminalCompositor) cellAtAbsolute(x, absoluteY, scrollback int) *uv.Cell {
	if absoluteY < scrollback {
		return c.emulator.ScrollbackCellAt(x, absoluteY)
	}
	return c.emulator.CellAt(x, absoluteY-scrollback)
}

type terminalMarkerStream struct {
	prefix  string
	pending []byte
}

func (s *terminalMarkerStream) Consume(data []byte, text func([]byte), marker func(string)) {
	input := make([]byte, 0, len(s.pending)+len(data))
	input = append(input, s.pending...)
	input = append(input, data...)
	s.pending = s.pending[:0]

	start := 0
	for index := 0; index < len(input); {
		if input[index] != '\x1b' {
			index++
			continue
		}
		if index+1 >= len(input) {
			text(input[start:index])
			s.pending = append(s.pending, input[index:]...)
			return
		}
		if input[index+1] != ']' {
			index += 2
			continue
		}

		end, terminatorLength := oscEnd(input, index+2)
		if end < 0 {
			text(input[start:index])
			if len(input)-index > 4096 {
				text(input[index:])
			} else {
				s.pending = append(s.pending, input[index:]...)
			}
			return
		}

		text(input[start:index])
		payload := string(input[index+2 : end])
		sequenceEnd := end + terminatorLength
		if event, ok := strings.CutPrefix(payload, s.prefix); ok {
			marker(event)
		} else {
			text(input[index:sequenceEnd])
		}
		index = sequenceEnd
		start = index
	}
	text(input[start:])
}

func oscEnd(data []byte, start int) (int, int) {
	for index := start; index < len(data); index++ {
		switch data[index] {
		case '\a':
			return index, 1
		case '\x1b':
			if index+1 >= len(data) {
				return -1, 0
			}
			if data[index+1] == '\\' {
				return index, 2
			}
		}
	}
	return -1, 0
}

func terminalOSCSequences(data []byte) []byte {
	var sequences []byte
	for index := 0; index+1 < len(data); {
		if data[index] != '\x1b' || data[index+1] != ']' {
			index++
			continue
		}
		end, terminatorLength := oscEnd(data, index+2)
		if end < 0 {
			break
		}
		sequenceEnd := end + terminatorLength
		selector, _, _ := bytes.Cut(data[index+2:end], []byte(";"))
		if !bytes.Equal(selector, []byte("8")) {
			sequences = append(sequences, data[index:sequenceEnd]...)
		}
		index = sequenceEnd
	}
	return sequences
}

func terminalMarker(marker, event string) string {
	return "\x1b]777;vuja;" + marker + ";" + event + "\a"
}

func terminalMarkerBytes(marker, event string) []byte {
	return []byte(terminalMarker(marker, event))
}

func resetsTerminalViewport(data []byte) bool {
	value := string(data)
	return strings.Contains(value, "\x1b[2J") ||
		strings.Contains(value, "\x1b[3J") ||
		strings.Contains(value, "\x1bc")
}

func lastLineBreak(data []byte) int {
	for index := len(data) - 1; index >= 0; index-- {
		if data[index] == '\n' {
			return index
		}
	}
	return -1
}

func isPromptRedrawPrelude(data []byte) bool {
	if strings.TrimSpace(ansi.Strip(string(data))) != "" {
		return false
	}
	value := string(data)
	return strings.Contains(value, "\x1b[2K") ||
		strings.Contains(value, "\x1b[0K") ||
		strings.Contains(value, "\x1b[1K") ||
		strings.Contains(value, "\x1b[H") ||
		strings.Contains(value, "\x1b[1;1H") ||
		strings.Contains(value, "\x1b[2J") ||
		strings.Contains(value, "\x1b[3J")
}

func endsWithIncompleteCSI(data []byte) bool {
	start := bytes.LastIndexByte(data, '\x1b')
	if start < 0 {
		return false
	}
	sequence := data[start:]
	if len(sequence) == 1 {
		return true
	}
	if sequence[1] != '[' {
		return false
	}
	for _, value := range sequence[2:] {
		if value >= 0x40 && value <= 0x7e {
			return false
		}
	}
	return true
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

package integration

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/spec"
	"golang.org/x/term"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	boxWidth                 = 76 // total visual width, corners included
	overlayHorizontalPadding = 2
)

var terminalSize = term.GetSize

func ComputeCursorCol(data []byte) int {
	col := 0
	i := 0
	n := len(data)
	for i < n {
		b := data[i]
		if b == '\r' {
			col = 0
			i++
			continue
		}
		if b == '\b' || b == 0x7f {
			col--
			if col < 0 {
				col = 0
			}
			i++
			continue
		}
		if b == '\t' {
			col = (col + 8) &^ 7
			i++
			continue
		}
		if b == '\033' {
			if i+1 < n && data[i+1] == '[' {
				j := i + 2
				for j < n && data[j] >= 0x20 && data[j] <= 0x3F {
					j++
				}
				if j < n {
					cmd := data[j]
					paramsStr := string(data[i+2 : j])
					paramsStr = strings.TrimLeft(paramsStr, "?>=")
					parts := strings.Split(paramsStr, ";")
					getParam := func(idx, def int) int {
						if idx < len(parts) && parts[idx] != "" {
							if v, err := strconv.Atoi(parts[idx]); err == nil && v > 0 {
								return v
							}
						}
						return def
					}
					switch cmd {
					case 'C':
						col += getParam(0, 1)
					case 'D':
						col -= getParam(0, 1)
						if col < 0 {
							col = 0
						}
					case 'G':
						col = max(getParam(0, 1)-1, 0)
					}
					i = j + 1
					continue
				}
				break
			} else if i+1 < n && data[i+1] == ']' {
				j := i + 2
				for j < n {
					if data[j] == '\007' {
						j++
						break
					}
					if data[j] == '\033' && j+1 < n && data[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			} else if i+1 < n && (data[i+1] == 'P' || data[i+1] == 'X' || data[i+1] == '^' || data[i+1] == '_') {
				j := i + 2
				for j < n {
					if data[j] == '\033' && j+1 < n && data[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			} else if i+1 < n {
				i += 2
				continue
			} else {
				break
			}
		}
		if b < 0x20 {
			i++
			continue
		}
		if b < 0x7f {
			col++
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		w := lipgloss.Width(string(r))
		col += w
		i += size
	}
	return col
}

type Theme struct {
	Background        lipgloss.TerminalColor
	SurfaceBackground lipgloss.TerminalColor
	Border            lipgloss.TerminalColor
	Accent            lipgloss.TerminalColor
	Muted             lipgloss.TerminalColor
	Text              lipgloss.TerminalColor
	TextSel           lipgloss.TerminalColor
	Match             lipgloss.TerminalColor
	Desc              lipgloss.TerminalColor
	DescSel           lipgloss.TerminalColor
	SelBg             lipgloss.TerminalColor
	ScrollInfo        lipgloss.TerminalColor
	GhostText         lipgloss.TerminalColor
}

var (
	themeOverride   *Theme
	themeOverrideMu sync.RWMutex
)

func ThemeFromConfig(colors config.ColorsConfig, isDarkBackground bool) Theme {
	palette := colors.Day
	if isDarkBackground {
		palette = colors.Night
	}
	color := func(value string) lipgloss.Color {
		return lipgloss.Color(value)
	}

	return Theme{
		Background:        color(palette.Background),
		SurfaceBackground: color(palette.SurfaceBackground),
		Border:            color(palette.Border),
		Accent:            color(palette.Accent),
		Muted:             color(palette.Muted),
		Text:              color(palette.Text),
		TextSel:           color(palette.TextSelected),
		Match:             color(palette.Match),
		Desc:              color(palette.Description),
		DescSel:           color(palette.DescriptionSelected),
		SelBg:             color(palette.SelectionBackground),
		ScrollInfo:        color(palette.ScrollInfo),
		GhostText:         color(palette.GhostText),
	}
}

func SetTheme(t Theme) {
	themeOverrideMu.Lock()
	defer themeOverrideMu.Unlock()
	themeOverride = &t
}

type Overlay struct {
	mu                 sync.Mutex
	theme              Theme
	Visible            bool
	Items              []spec.Suggestion
	Cursor             int
	StartIdx           int
	LastGhostLen       int
	PromptRows         int
	BottomPrompt       bool
	TypedQuery         string
	ContextLabel       string
	RichHistory        bool
	HistoryResults     []RichHistoryResult
	UserNavigated      bool
	PromptLen          int
	LastWindowSize     int
	LastMenuTopRow     int
	LastMenuBottom     bool
	LastTerminalHeight int
}

func (o *Overlay) SetPromptRows(rows int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.PromptRows = max(rows, 1)
}

func (o *Overlay) SetBottomPrompt(enabled bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.BottomPrompt = enabled
}

func (o *Overlay) InvalidateGeometry() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.LastWindowSize = 0
}

func (o *Overlay) BottomRegion() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.BottomPrompt {
		return 0, 0
	}
	top, bottom := 0, -1
	if o.LastWindowSize > 0 {
		top = o.LastMenuTopRow
		bottom = top + o.LastWindowSize + 1
	}
	if o.LastGhostLen > 0 && o.LastTerminalHeight > 0 {
		ghostRow := o.LastTerminalHeight - 1
		if o.PromptRows > 1 {
			ghostRow--
		}
		if bottom < 0 || ghostRow < top {
			top = ghostRow
		}
		bottom = max(bottom, ghostRow)
	}
	if bottom < top {
		return 0, 0
	}
	return top, bottom - top + 1
}

func (o *Overlay) ClearForRedraw() string {
	o.mu.Lock()
	bottomPrompt := o.BottomPrompt
	o.mu.Unlock()
	if bottomPrompt {
		return ""
	}
	return o.Clear()
}

func (o *Overlay) SetPromptLen(l int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.PromptLen != l {
		logger.Debugf("SetPromptLen: %d -> %d", o.PromptLen, l)
		o.PromptLen = l
	}
}

func NewOverlay(isDarkBackground bool) *Overlay {
	theme := ThemeFromConfig(config.Get().UI.Colors, isDarkBackground)
	themeOverrideMu.RLock()
	if themeOverride != nil {
		theme = *themeOverride
	}
	themeOverrideMu.RUnlock()
	return &Overlay{
		theme:        theme,
		Visible:      false,
		Cursor:       0,
		StartIdx:     0,
		BottomPrompt: config.Get().UI.PromptPosition == "bottom",
	}
}

func (o *Overlay) UpdateItems(items []spec.Suggestion) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Items = items
	o.ContextLabel = ""
	o.RichHistory = false
	o.HistoryResults = nil
	o.Visible = len(o.Items) > 0
	o.Cursor = 0
	o.StartIdx = 0
}

func (o *Overlay) IsVisible() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Visible
}

func (o *Overlay) GetUserNavigated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.UserNavigated
}

func (o *Overlay) SetUserNavigated(v bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.UserNavigated = v
}

func (o *Overlay) GetTypedQuery() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.TypedQuery
}

func (o *Overlay) GetCurrentCmd() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.Items) > 0 && o.Cursor >= 0 && o.Cursor < len(o.Items) {
		return o.Items[o.Cursor].Cmd
	}
	return ""
}

func (o *Overlay) GetTopCmd() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.Items) > 0 {
		return o.Items[0].Cmd
	}
	return ""
}

func (o *Overlay) SuggestionCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.Items)
}

func (o *Overlay) FocusFirst() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.Visible || len(o.Items) == 0 {
		return ""
	}
	o.UserNavigated = true
	o.Cursor = 0
	o.StartIdx = 0
	return o.Items[0].Cmd
}

func (o *Overlay) CycleCursor() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.Visible || len(o.Items) == 0 {
		return ""
	}
	o.UserNavigated = true
	o.Cursor = (o.Cursor + 1) % len(o.Items)
	if o.Cursor == 0 {
		o.StartIdx = 0
	}
	return o.Items[o.Cursor].Cmd
}

func (o *Overlay) Show() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.UserNavigated = false
	o.Visible = true
}

func (o *Overlay) ResetCursor() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Cursor = 0
}

func (o *Overlay) SetQueryAndItems(query string, items []spec.Suggestion) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.TypedQuery = query
	o.ContextLabel = ""
	o.RichHistory = false
	o.HistoryResults = nil
	o.UserNavigated = false
	o.Items = items
	o.Visible = len(o.Items) > 0
	o.Cursor = 0
	o.StartIdx = 0
}

func (o *Overlay) SetHistorySearch(query string, label string, items []spec.Suggestion) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.TypedQuery = query
	o.ContextLabel = strings.TrimSpace(label)
	if query != "" {
		o.ContextLabel += ": " + query
	}
	o.UserNavigated = false
	o.Items = items
	o.RichHistory = false
	o.HistoryResults = nil
	o.Visible = len(items) > 0
	o.Cursor = 0
	o.StartIdx = 0
}

func (o *Overlay) SetRichHistorySearch(query string, label string, results []RichHistoryResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.TypedQuery = query
	o.ContextLabel = strings.TrimSpace(label)
	if query != "" {
		o.ContextLabel += ": " + query
	}
	o.UserNavigated = false
	o.RichHistory = true
	o.HistoryResults = append([]RichHistoryResult(nil), results...)
	o.Items = make([]spec.Suggestion, 0, len(results))
	for _, result := range results {
		o.Items = append(o.Items, spec.Suggestion{
			Cmd:        result.Command,
			Desc:       "history",
			Icon:       "history",
			Source:     "history",
			Confidence: 80,
		})
	}
	o.Visible = true
	o.Cursor = 0
	o.StartIdx = 0
}

func (o *Overlay) InjectAISuggestion(sugg spec.Suggestion) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.TypedQuery == "" {
		return false
	}
	if o.UserNavigated {
		return false
	}

	var currentConf int
	if len(o.Items) > 0 {
		currentConf = o.Items[0].Confidence
		if currentConf == 0 {
			if o.Items[0].Source == "history" {
				currentConf = 70
			} else {
				currentConf = 50
			}
		}
	}

	if !strings.HasPrefix(strings.ToLower(sugg.Cmd), strings.ToLower(o.TypedQuery)) {
		return false
	}
	if sugg.Confidence <= currentConf && len(o.Items) > 0 {
		return false
	}

	if len(o.Items) == 0 {
		o.Items = []spec.Suggestion{sugg}
	} else if strings.EqualFold(o.Items[0].Cmd, sugg.Cmd) {
		if o.Visible && o.Items[0].Confidence == sugg.Confidence {
			return false
		}
		o.Items[0] = sugg
	} else {
		o.Items = append([]spec.Suggestion{sugg}, o.Items...)
		if len(o.Items) > 100 {
			o.Items = o.Items[:100]
		}
	}
	o.Visible = true
	o.Cursor = 0
	o.StartIdx = 0
	return true
}

func (o *Overlay) ClearGhostLen() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	l := o.LastGhostLen
	o.LastGhostLen = 0
	return l
}

func (o *Overlay) MoveCursor(dir string) (moved bool, selectedCmd string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.Visible || len(o.Items) == 0 {
		return false, ""
	}
	o.UserNavigated = true
	oldCursor := o.Cursor
	if dir == "up" {
		o.Cursor--
		if o.Cursor < 0 {
			o.Cursor = 0
		}
	} else {
		o.Cursor++
		if o.Cursor >= len(o.Items) {
			o.Cursor = len(o.Items) - 1
		}
	}
	if o.Cursor == oldCursor {
		return false, ""
	}
	return true, o.Items[o.Cursor].Cmd
}

func (o *Overlay) SetHistoryList(items []spec.Suggestion, startAtBottom bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.TypedQuery = ""
	o.ContextLabel = "history"
	o.RichHistory = false
	o.HistoryResults = nil
	o.UserNavigated = true
	o.Items = items
	o.Visible = len(o.Items) > 0
	if startAtBottom && len(o.Items) > 0 {
		o.Cursor = len(o.Items) - 1
	} else {
		o.Cursor = 0
	}
	o.StartIdx = 0
	if len(o.Items) > 0 && o.Cursor >= 0 && o.Cursor < len(o.Items) {
		return o.Items[o.Cursor].Cmd
	}
	return ""
}

func fixedWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	visualWidth := lipgloss.Width(s)
	if visualWidth == width {
		return s
	}
	if visualWidth < width {
		return s + strings.Repeat(" ", width-visualWidth)
	}
	var sb strings.Builder
	currentWidth := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if currentWidth+rw > width-1 {
			break
		}
		sb.WriteRune(r)
		currentWidth += rw
	}
	sb.WriteString("…")
	rem := width - lipgloss.Width(sb.String())
	if rem > 0 {
		sb.WriteString(strings.Repeat(" ", rem))
	}
	return sb.String()
}

func truncateToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW == 1 {
		return "…"
	}
	var sb strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > maxW-1 { // leave 1 column for '…'
			break
		}
		sb.WriteRune(r)
		w += rw
	}
	sb.WriteRune('…')
	return sb.String()
}

func visibleItemLimit(configured int, terminalRows int) int {
	if configured <= 0 {
		configured = 6
	}
	available := terminalRows - 3
	if available < 1 {
		available = 1
	}
	return min(configured, available)
}

func (o *Overlay) GetGhostText(buffer string, cursorAtEnd bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ghostText(buffer, cursorAtEnd)
}

func (o *Overlay) ghostText(buffer string, cursorAtEnd bool) string {
	if !o.Visible || len(o.Items) == 0 || !cursorAtEnd || buffer == "" {
		return ""
	}

	var topCmd string
	if o.Cursor >= 0 && o.Cursor < len(o.Items) {
		topCmd = o.Items[o.Cursor].Cmd
	} else {
		topCmd = o.Items[0].Cmd
	}

	if strings.HasPrefix(strings.ToLower(topCmd), strings.ToLower(buffer)) {
		return topCmd[len(buffer):]
	}
	return ""
}

func (o *Overlay) GetNextGhostText(buffer string, cursorAtEnd bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return nextGhostChunk(o.ghostText(buffer, cursorAtEnd))
}

func nextGhostChunk(ghost string) string {
	seenContent := false
	for index, char := range ghost {
		if char == '/' || char == '\\' {
			return ghost[:index+1]
		}
		if unicode.IsSpace(char) {
			if seenContent {
				return ghost[:index+len(string(char))]
			}
			continue
		}
		seenContent = true
	}
	return ghost
}

func (o *Overlay) RenderGhostText(buffer string, userNavigated bool, cursorAtEnd bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.Visible || len(o.Items) == 0 {
		if o.LastGhostLen > 0 {
			padLen := o.LastGhostLen + 4
			o.LastGhostLen = 0
			return "\0337" + o.renderGhostClear(padLen) + "\0338"
		}
		return ""
	}

	var s strings.Builder
	ghostText := ""
	if cursorAtEnd && buffer != "" {
		var topCmd string
		if o.Cursor >= 0 && o.Cursor < len(o.Items) {
			topCmd = o.Items[o.Cursor].Cmd
		} else {
			topCmd = o.Items[0].Cmd
		}
		if strings.HasPrefix(strings.ToLower(topCmd), strings.ToLower(buffer)) {
			ghostText = topCmd[len(buffer):]
		}
	}

	if ghostText != "" {
		width, _, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || width <= 0 {
			width = 120
		}
		cursorCol := o.PromptLen + lipgloss.Width(buffer)
		availableCols := width - cursorCol
		if availableCols <= 0 {
			ghostText = ""
		} else if lipgloss.Width(ghostText) > availableCols {
			ghostText = truncateToWidth(ghostText, availableCols)
		}
	}

	if ghostText == "" && o.LastGhostLen == 0 {
		return ""
	}

	ghostWidth := lipgloss.Width(ghostText)
	padLen := max(o.LastGhostLen-ghostWidth, 0)
	if o.LastGhostLen > 0 {
		padLen += 4
	}

	s.WriteString("\0337")
	if ghostText != "" {
		style := lipgloss.NewStyle().Foreground(o.theme.GhostText)
		if o.BottomPrompt {
			style = style.Background(o.theme.SurfaceBackground)
		}
		styled := style.Render(ghostText)
		s.WriteString(styled)
	}
	if padLen > 0 {
		s.WriteString(o.renderGhostClear(padLen))
	}
	s.WriteString("\0338")
	o.LastGhostLen = ghostWidth

	return s.String()
}

func (o *Overlay) renderGhostClear(width int) string {
	if width <= 0 {
		return ""
	}
	clear := strings.Repeat(" ", width)
	if !o.BottomPrompt {
		return clear
	}
	return lipgloss.NewStyle().Background(o.theme.SurfaceBackground).Render(clear)
}

func renderMatchedTitle(title, typed string, selected bool, w int, t Theme) string {
	textColor := t.Text
	if selected {
		textColor = t.TextSel
	}

	base := lipgloss.NewStyle().Foreground(textColor)
	match := lipgloss.NewStyle().Foreground(t.Match).Bold(true)
	if selected {
		base = base.Background(t.SelBg)
		match = match.Background(t.SelBg)
	}

	display := fixedWidth(title, w)

	if typed == "" || !strings.HasPrefix(strings.ToLower(display), strings.ToLower(typed)) {
		return base.Render(display)
	}

	typedRunes := []rune(typed)
	displayRunes := []rune(display)
	matchLen := min(len(typedRunes), len(displayRunes))
	return match.Render(string(displayRunes[:matchLen])) + base.Render(string(displayRunes[matchLen:]))
}

func keybindingLabel(binding string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(binding)), "+")
	if len(parts) == 0 || parts[0] == "" || parts[0] == "none" {
		return ""
	}
	for i, part := range parts {
		switch part {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "meta":
			parts[i] = "Meta"
		case "shift":
			parts[i] = "Shift"
		case "tab":
			parts[i] = "Tab"
		case "right":
			parts[i] = "Right"
		default:
			parts[i] = strings.ToUpper(part)
		}
	}
	return "<" + strings.Join(parts, "+") + ">"
}

func (o *Overlay) Render() string {
	return o.draw()
}

func (o *Overlay) draw() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	emptyRichHistory := o.RichHistory && len(o.Items) == 0
	if !o.Visible || (len(o.Items) == 0 && !emptyRichHistory) {
		return ""
	}

	t := o.theme
	border := lipgloss.NewStyle().Foreground(t.Border)
	scrollStyle := lipgloss.NewStyle().Foreground(t.ScrollInfo)

	var s strings.Builder
	s.WriteString("\033[?7l")

	typedLen := len([]rune(o.TypedQuery))
	targetCol := o.PromptLen + typedLen

	width, height, err := terminalSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 24
	}
	o.LastTerminalHeight = height
	bottomPrompt := o.BottomPrompt
	if bottomPrompt {
		targetCol = 0
	}
	promptRows := max(o.PromptRows, 1)
	availableHeight := height
	if bottomPrompt {
		availableHeight -= promptRows
	}
	if bottomPrompt && (width < 12 || availableHeight < 3) {
		o.LastWindowSize = 0
		s.WriteString("\033[?7h")
		return s.String()
	}
	currentBoxWidth := boxWidth
	if bottomPrompt {
		currentBoxWidth = width
	} else if o.RichHistory && width < currentBoxWidth {
		currentBoxWidth = width
	}
	if targetCol+currentBoxWidth > width {
		targetCol = width - currentBoxWidth
	}
	if targetCol < 0 {
		targetCol = 0
	}
	logger.Debugf("Overlay draw: pLen=%d, typedLen=%d, targetCol=%d, width=%d", o.PromptLen, typedLen, targetCol, width)

	s.WriteString("\0337")

	itemCount := len(o.Items)
	if emptyRichHistory {
		itemCount = 1
	}
	windowSize := min(itemCount, visibleItemLimit(config.Get().UI.MaxHeight, availableHeight))
	o.LastWindowSize = windowSize

	scrolloffUp := 1
	if windowSize <= 3 {
		scrolloffUp = 0
	}

	if o.Cursor < o.StartIdx+scrolloffUp {
		o.StartIdx = o.Cursor - scrolloffUp
	}
	if o.Cursor >= o.StartIdx+windowSize {
		o.StartIdx = o.Cursor - windowSize + 1
	}
	if o.StartIdx < 0 {
		o.StartIdx = 0
	}
	if o.StartIdx > itemCount-windowSize {
		o.StartIdx = itemCount - windowSize
	}
	if o.StartIdx < 0 {
		o.StartIdx = 0
	}

	start := o.StartIdx
	end := start + windowSize
	totalLines := windowSize + 2
	menuTopRow := height - promptRows - totalLines
	if menuTopRow < 0 {
		menuTopRow = 0
	}
	o.LastMenuTopRow = menuTopRow
	o.LastMenuBottom = bottomPrompt

	if !bottomPrompt {
		for range totalLines {
			s.WriteByte('\n')
		}
		fmt.Fprintf(&s, "\033[%dA", totalLines)
	}

	s.WriteString("\0337")

	moveToRow := func(row int) {
		s.WriteString("\0338")
		if bottomPrompt {
			fmt.Fprintf(&s, "\033[%d;1H", menuTopRow+row+1)
		} else {
			fmt.Fprintf(&s, "\033[%dB", row+1)
		}
		s.WriteString("\r")
		if targetCol > 0 {
			fmt.Fprintf(&s, "\033[%dC", targetCol)
		}
	}

	inner := currentBoxWidth
	if !bottomPrompt {
		inner -= 2 // width between the two border pipes/corners
	}

	style := strings.ToLower(config.Get().UI.Style)
	isClassic := style == "classic" || style == "minimal" || style == "minimalist"

	// Header with scroll counter.
	moveToRow(0)
	s.WriteString("\033[2K")

	scrollInfo := strings.TrimSpace(o.ContextLabel)
	if len(o.Items) > windowSize {
		counter := fmt.Sprintf("%d/%d", o.Cursor+1, len(o.Items))
		if scrollInfo != "" {
			scrollInfo += " • "
		}
		scrollInfo += counter
	}
	if scrollInfo != "" {
		scrollInfo = " " + truncateToWidth(scrollInfo, inner-4) + " "
	}
	leftDash := 3
	if isClassic && scrollInfo != "" {
		leftDash = (inner - len(scrollInfo)) / 2
	}
	rightDash := inner - leftDash - len(scrollInfo)
	if scrollInfo == "" {
		leftDash = 0
		rightDash = inner
	}
	if bottomPrompt {
		if scrollInfo != "" {
			s.WriteString(strings.Repeat(" ", overlayHorizontalPadding))
			s.WriteString(scrollStyle.Render(strings.TrimSpace(scrollInfo)))
		}
	} else {
		fmt.Fprintf(&s, "%s%s%s%s%s",
			border.Render("╭"),
			border.Render(strings.Repeat("─", leftDash)),
			scrollStyle.Render(scrollInfo),
			border.Render(strings.Repeat("─", rightDash)),
			border.Render("╮"),
		)
	}

	// Suggestion rows.
	descW := min(24, max(inner/3, 0))
	padGap := 2
	markerGlyph := " "
	if bottomPrompt {
		markerGlyph = overlayPromptMarker()
	}
	markerW := lipgloss.Width(markerGlyph)
	iconW := 2
	if isClassic || !config.Get().UI.NerdFonts {
		iconW = 0
	}
	leadingPadding := 1
	trailingPadding := 1
	if bottomPrompt {
		leadingPadding = overlayHorizontalPadding
	}
	for i := start; i < end; i++ {
		moveToRow((i - start) + 1)
		s.WriteString("\033[2K")

		left := border.Render("│")
		right := border.Render("│")
		if bottomPrompt {
			left = ""
			right = ""
		}
		bg := lipgloss.NewStyle()
		selected := !emptyRichHistory && i == o.Cursor
		if selected {
			bg = bg.Background(t.SelBg)
		}

		if emptyRichHistory {
			message := fixedWidth("No history matches in this scope", max(inner-leadingPadding-trailingPadding, 1))
			fmt.Fprintf(&s, "%s%s%s%s",
				left,
				lipgloss.NewStyle().Foreground(t.Muted).Render(strings.Repeat(" ", leadingPadding)+message),
				lipgloss.NewStyle().Foreground(t.Muted).Render(strings.Repeat(" ", trailingPadding)),
				right,
			)
			continue
		}

		it := o.Items[i]

		marker := markerGlyph
		markerStyle := bg.Foreground(t.Muted)
		if selected {
			if !bottomPrompt {
				marker = "▶"
			}
			markerStyle = bg.Foreground(t.Accent).Bold(true)
		}

		if o.RichHistory && i < len(o.HistoryResults) {
			rowWidth := max(inner-leadingPadding-markerW-1-trailingPadding, 1)
			row := renderRichHistoryRow(o.HistoryResults[i], selected, rowWidth, t, config.Get().UI.History)
			fmt.Fprintf(&s, "%s%s%s%s%s%s%s",
				left,
				bg.Render(strings.Repeat(" ", leadingPadding)),
				markerStyle.Render(marker),
				bg.Render(" "),
				row,
				bg.Render(strings.Repeat(" ", trailingPadding)),
				right,
			)
			continue
		}

		iconGlyph := lookupIcon(it.Icon)
		iconColor := t.Muted
		if selected {
			iconColor = t.Accent
		}
		iconStr := bg.Foreground(iconColor).Render(fixedWidth(iconGlyph, iconW))

		iconSection := ""
		if iconW > 0 {
			iconSection = iconStr + bg.Render(" ")
		}

		trailing := ""
		trailingW := descW
		if bottomPrompt {
			trailing = renderSuggestionBadge(suggestionBadgeLabel(it), selected, t)
			trailingW = lipgloss.Width(trailing)
		} else {
			trailing = renderLegacySuggestionDescription(it, selected, descW, bg, t, isClassic)
		}
		titleW := max(
			inner-leadingPadding-markerW-1-lipgloss.Width(iconSection)-padGap-trailingW-trailingPadding,
			1,
		)
		title := renderMatchedTitle(it.Cmd, o.TypedQuery, selected, titleW, t)

		fmt.Fprintf(&s, "%s%s%s%s%s%s%s%s%s%s",
			left,
			bg.Render(strings.Repeat(" ", leadingPadding)),
			markerStyle.Render(marker),
			bg.Render(" "),
			iconSection,
			title,
			bg.Render(strings.Repeat(" ", padGap)),
			trailing,
			bg.Render(strings.Repeat(" ", trailingPadding)),
			right,
		)
	}

	// Footer with shortcut hints.
	moveToRow(windowSize + 1)
	s.WriteString("\033[2K")

	footerInfo := ""
	if !isClassic {
		keyStyle := lipgloss.NewStyle().Foreground(t.ScrollInfo).Bold(true)
		textStyle := lipgloss.NewStyle().Foreground(t.ScrollInfo)
		var hints []string
		if label := keybindingLabel(config.Get().Keybindings.Accept); label != "" {
			hints = append(hints, keyStyle.Render(label)+textStyle.Render(" Next/Accept"))
		}
		if label := keybindingLabel(config.Get().Keybindings.HistorySearch); label != "" {
			hints = append(hints, keyStyle.Render(label)+textStyle.Render(" Mode"))
		}
		if len(hints) > 0 {
			footerInfo = " " + strings.Join(hints, textStyle.Render(" • ")) + " "
		}
	}

	footerRunes := lipgloss.Width(footerInfo)
	rightDash = 2
	leftDash = inner - footerRunes - rightDash
	if footerInfo == "" {
		leftDash = 0
		rightDash = inner
	}
	if leftDash < 0 {
		leftDash = 0
	}
	if bottomPrompt {
		if footerInfo != "" {
			footerWidth := lipgloss.Width(footerInfo)
			s.WriteString(strings.Repeat(" ", max(currentBoxWidth-footerWidth-trailingPadding, 0)))
			s.WriteString(footerInfo)
		}
	} else {
		fmt.Fprintf(&s, "%s%s%s%s%s",
			border.Render("╰"),
			border.Render(strings.Repeat("─", leftDash)),
			footerInfo,
			border.Render(strings.Repeat("─", rightDash)),
			border.Render("╯"),
		)
	}

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	return s.String()
}

func renderLegacySuggestionDescription(
	suggestion spec.Suggestion,
	selected bool,
	width int,
	background lipgloss.Style,
	theme Theme,
	classic bool,
) string {
	descriptionColor := theme.Desc
	if selected {
		descriptionColor = theme.DescSel
	}
	if classic {
		text := suggestion.Desc
		if suggestion.Icon == "alias" {
			text = "alias: " + text
		}
		return background.Foreground(descriptionColor).Render(fixedWidth(text, width))
	}

	label := ""
	color := theme.Muted
	switch suggestion.Icon {
	case "alias":
		label, color = "alias", theme.ScrollInfo
	case "history":
		label, color = "history", theme.Accent
	case "system":
		label, color = "system", theme.Border
	}
	if label == "" {
		return background.Foreground(descriptionColor).Render(fixedWidth(suggestion.Desc, width))
	}
	badgeStyle := lipgloss.NewStyle().Background(theme.SelBg).Foreground(color)
	if selected {
		badgeStyle = lipgloss.NewStyle().Background(color).Foreground(theme.Background).Bold(true)
	}
	badge := badgeStyle.Render(" " + label + " ")
	remaining := max(width-lipgloss.Width(badge), 0)
	if label == "alias" && remaining > 0 {
		return badge + background.Render(" ") + background.Foreground(descriptionColor).Render(fixedWidth(suggestion.Desc, max(remaining-1, 0)))
	}
	return badge + background.Render(strings.Repeat(" ", remaining))
}

func overlayPromptMarker() string {
	marker := strings.TrimSpace(config.Get().UI.Chatbox.Prompt)
	if marker == "" {
		return "›"
	}
	return marker
}

func suggestionBadgeLabel(suggestion spec.Suggestion) string {
	switch suggestion.Source {
	case "history":
		return "history"
	case "argument":
		return "learned"
	case "directory-index":
		return "visited"
	case "project":
		return "project"
	case "pin":
		return "pinned"
	case "workspace":
		return "workspace"
	case "recovery":
		return "recovery"
	case "ai":
		return "ai"
	case "filesystem":
		if suggestion.Desc == "directory" || suggestion.Desc == "file" {
			return suggestion.Desc
		}
		return "filesystem"
	}
	switch suggestion.Icon {
	case "history", "alias", "system":
		return suggestion.Icon
	}
	if suggestion.Desc == "directory" || suggestion.Desc == "file" {
		return suggestion.Desc
	}
	return "command"
}

func renderSuggestionBadge(label string, selected bool, theme Theme) string {
	color := theme.Muted
	switch label {
	case "history":
		color = theme.Accent
	case "learned":
		color = theme.ScrollInfo
	case "visited":
		color = theme.Border
	case "directory":
		color = theme.Match
	case "file", "filesystem":
		color = theme.Desc
	case "pinned", "workspace":
		color = theme.Match
	case "project", "alias":
		color = theme.Desc
	}
	style := lipgloss.NewStyle().Background(color).Foreground(theme.Background)
	if selected {
		style = style.Bold(true)
	}
	return style.Render(" " + label + " ")
}

func (o *Overlay) Clear() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	var s strings.Builder
	s.WriteString("\033[?7l")
	s.WriteString("\0337")

	o.clearMenuRows(&s)

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	o.LastWindowSize = 0
	return s.String()
}

func (o *Overlay) HideMenu(query string) string {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.TypedQuery = query
	o.ContextLabel = ""
	if !o.Visible && len(o.Items) == 0 && o.LastGhostLen == 0 {
		return ""
	}

	o.Visible = false
	o.Items = nil
	o.UserNavigated = false
	o.Cursor = 0
	o.StartIdx = 0

	var s strings.Builder
	s.WriteString("\033[?7l")

	if o.LastGhostLen > 0 {
		s.WriteString("\0337")
		s.WriteString(o.renderGhostClear(o.LastGhostLen + 10))
		s.WriteString("\0338")
		o.LastGhostLen = 0
	}

	s.WriteString("\0337")

	o.clearMenuRows(&s)

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	o.LastWindowSize = 0
	return s.String()
}

func (o *Overlay) ClearAndDisable() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.Visible && len(o.Items) == 0 && o.LastGhostLen == 0 {
		return ""
	}

	o.Visible = false
	o.Items = nil
	o.TypedQuery = ""
	o.ContextLabel = ""
	o.UserNavigated = false
	o.Cursor = 0
	o.StartIdx = 0

	var s strings.Builder
	s.WriteString("\033[?7l")

	if o.LastGhostLen > 0 {
		s.WriteString("\0337")
		s.WriteString(o.renderGhostClear(o.LastGhostLen + 10))
		s.WriteString("\0338")
		o.LastGhostLen = 0
	}

	s.WriteString("\0337")

	o.clearMenuRows(&s)

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	o.LastWindowSize = 0
	return s.String()
}

func (o *Overlay) clearMenuRows(s *strings.Builder) {
	if o.LastWindowSize <= 0 {
		return
	}
	for i := range o.LastWindowSize + 2 {
		s.WriteString("\0338")
		if o.LastMenuBottom {
			fmt.Fprintf(s, "\033[%d;1H", o.LastMenuTopRow+i+1)
		} else {
			fmt.Fprintf(s, "\033[%dB", i+1)
		}
		s.WriteString("\r\033[2K")
	}
}

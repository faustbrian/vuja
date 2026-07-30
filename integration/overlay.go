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
	boxWidth = 76 // total visual width, corners included
)

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
	Background lipgloss.TerminalColor
	Border     lipgloss.TerminalColor
	Accent     lipgloss.TerminalColor
	Muted      lipgloss.TerminalColor
	Text       lipgloss.TerminalColor
	TextSel    lipgloss.TerminalColor
	Match      lipgloss.TerminalColor
	Desc       lipgloss.TerminalColor
	DescSel    lipgloss.TerminalColor
	SelBg      lipgloss.TerminalColor
	ScrollInfo lipgloss.TerminalColor
	GhostText  lipgloss.TerminalColor
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
		Background: color(palette.Background),
		Border:     color(palette.Border),
		Accent:     color(palette.Accent),
		Muted:      color(palette.Muted),
		Text:       color(palette.Text),
		TextSel:    color(palette.TextSelected),
		Match:      color(palette.Match),
		Desc:       color(palette.Description),
		DescSel:    color(palette.DescriptionSelected),
		SelBg:      color(palette.SelectionBackground),
		ScrollInfo: color(palette.ScrollInfo),
		GhostText:  color(palette.GhostText),
	}
}

func SetTheme(t Theme) {
	themeOverrideMu.Lock()
	defer themeOverrideMu.Unlock()
	themeOverride = &t
}

type Overlay struct {
	mu             sync.Mutex
	theme          Theme
	Visible        bool
	Items          []spec.Suggestion
	Cursor         int
	StartIdx       int
	LastGhostLen   int
	TypedQuery     string
	ContextLabel   string
	RichHistory    bool
	HistoryResults []RichHistoryResult
	UserNavigated  bool
	PromptLen      int
	LastWindowSize int
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
	return &Overlay{theme: theme, Visible: false, Cursor: 0, StartIdx: 0}
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
			return "\0337" + strings.Repeat(" ", padLen) + "\0338"
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
		styled := lipgloss.NewStyle().Foreground(o.theme.GhostText).Render(ghostText)
		s.WriteString(styled)
	}
	if padLen > 0 {
		s.WriteString(strings.Repeat(" ", padLen))
	}
	s.WriteString("\0338")
	o.LastGhostLen = ghostWidth

	return s.String()
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

	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 24
	}
	currentBoxWidth := boxWidth
	if o.RichHistory && width < currentBoxWidth {
		currentBoxWidth = max(width, 12)
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
	windowSize := min(itemCount, visibleItemLimit(config.Get().UI.MaxHeight, height))
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

	for range totalLines {
		s.WriteByte('\n')
	}
	fmt.Fprintf(&s, "\033[%dA", totalLines)

	s.WriteString("\0337")

	moveToTarget := func() {
		s.WriteString("\r")
		if targetCol > 0 {
			fmt.Fprintf(&s, "\033[%dC", targetCol)
		}
	}

	inner := currentBoxWidth - 2 // width between the two border pipes/corners

	style := strings.ToLower(config.Get().UI.Style)
	isClassic := style == "classic" || style == "minimal" || style == "minimalist"

	// top side border with scroll counter
	s.WriteString("\0338")
	fmt.Fprintf(&s, "\033[%dB", 1)
	s.WriteString("\033[2K")
	moveToTarget()

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
	fmt.Fprintf(&s, "%s%s%s%s%s",
		border.Render("╭"),
		border.Render(strings.Repeat("─", leftDash)),
		scrollStyle.Render(scrollInfo),
		border.Render(strings.Repeat("─", rightDash)),
		border.Render("╮"),
	)

	// left and right side border with item rows
	descW := 24
	padGap := 2
	markerW := 1
	iconW := 2
	if isClassic || !config.Get().UI.NerdFonts {
		iconW = 0
	}
	sidePad := 1
	titleW := inner - sidePad*2 - markerW - 1 - iconW
	if iconW > 0 {
		titleW--
	}
	titleW = titleW - padGap - descW

	for i := start; i < end; i++ {
		s.WriteString("\0338")
		fmt.Fprintf(&s, "\033[%dB", (i-start)+2)
		s.WriteString("\033[2K")
		moveToTarget()

		if emptyRichHistory {
			message := fixedWidth("No history matches in this scope", max(inner-2, 1))
			fmt.Fprintf(&s, "%s%s%s%s",
				border.Render("│"),
				lipgloss.NewStyle().Foreground(t.Muted).Render(" "+message),
				lipgloss.NewStyle().Foreground(t.Muted).Render(" "),
				border.Render("│"),
			)
			continue
		}

		it := o.Items[i]
		selected := i == o.Cursor

		left := border.Render("│")
		right := border.Render("│")

		bg := lipgloss.NewStyle()
		if selected {
			bg = bg.Background(t.SelBg)
		}

		marker := " "
		markerStyle := bg.Foreground(t.Muted)
		if selected {
			marker = "▶"
			markerStyle = bg.Foreground(t.Accent).Bold(true)
		}

		if o.RichHistory && i < len(o.HistoryResults) {
			rowWidth := max(inner-4, 1)
			row := renderRichHistoryRow(o.HistoryResults[i], selected, rowWidth, t, config.Get().UI.History)
			fmt.Fprintf(&s, "%s%s%s%s%s%s%s",
				left,
				bg.Render(" "),
				markerStyle.Render(marker),
				bg.Render(" "),
				row,
				bg.Render(" "),
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

		title := renderMatchedTitle(it.Cmd, o.TypedQuery, selected, titleW, t)

		descColor := t.Desc
		if selected {
			descColor = t.DescSel
		}

		var desc string
		if isClassic {
			if it.Icon == "alias" {
				desc = bg.Foreground(descColor).Render(fixedWidth("alias: "+it.Desc, descW))
			} else {
				desc = bg.Foreground(descColor).Render(fixedWidth(it.Desc, descW))
			}
		} else {
			switch it.Icon {
			case "alias":
				boxStyle := lipgloss.NewStyle().Background(t.SelBg).Foreground(t.ScrollInfo)
				if selected {
					boxStyle = lipgloss.NewStyle().Background(t.ScrollInfo).Foreground(t.Background).Bold(true)
				}
				tag := boxStyle.Render(" alias ")
				tw := lipgloss.Width(tag)
				rem := max(descW-tw-1, 0)
				desc = tag + bg.Render(" ") + bg.Foreground(descColor).Render(fixedWidth(it.Desc, rem))
			case "history":
				boxStyle := lipgloss.NewStyle().Background(t.SelBg).Foreground(t.Accent)
				if selected {
					boxStyle = lipgloss.NewStyle().Background(t.Accent).Foreground(t.Background).Bold(true)
				}
				tag := boxStyle.Render(" history ")
				tw := lipgloss.Width(tag)
				rem := max(descW-tw, 0)
				desc = tag + bg.Render(strings.Repeat(" ", rem))
			case "system":
				boxStyle := lipgloss.NewStyle().Background(t.SelBg).Foreground(t.Border)
				if selected {
					boxStyle = lipgloss.NewStyle().Background(t.Border).Foreground(t.Background).Bold(true)
				}
				tag := boxStyle.Render(" system ")
				tw := lipgloss.Width(tag)
				rem := max(descW-tw, 0)
				desc = tag + bg.Render(strings.Repeat(" ", rem))
			default:
				desc = bg.Foreground(descColor).Render(fixedWidth(it.Desc, descW))
			}
		}

		iconSection := ""
		if iconW > 0 {
			iconSection = iconStr + bg.Render(" ")
		}

		fmt.Fprintf(&s, "%s%s%s%s%s%s%s%s%s%s",
			left,
			bg.Render(" "),
			markerStyle.Render(marker),
			bg.Render(" "),
			iconSection,
			title,
			bg.Render(strings.Repeat(" ", padGap)),
			desc,
			bg.Render(" "),
			right,
		)
	}

	// bottom side border with footer shortcut hints
	s.WriteString("\0338")
	fmt.Fprintf(&s, "\033[%dB", windowSize+2)
	s.WriteString("\033[2K")
	moveToTarget()

	footerInfo := ""
	if !isClassic {
		keyStyle := lipgloss.NewStyle().Foreground(t.ScrollInfo).Bold(true)
		tabKey := keyStyle.Render("<Tab>")
		ctrlRKey := keyStyle.Render("<Ctrl+R>")
		acceptText := lipgloss.NewStyle().Foreground(t.ScrollInfo).Render(" Accept")
		modeText := lipgloss.NewStyle().Foreground(t.ScrollInfo).Render(" Mode")
		footerInfo = fmt.Sprintf(" %s%s • %s%s ", tabKey, acceptText, ctrlRKey, modeText)
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
	fmt.Fprintf(&s, "%s%s%s%s%s",
		border.Render("╰"),
		border.Render(strings.Repeat("─", leftDash)),
		footerInfo,
		border.Render(strings.Repeat("─", rightDash)),
		border.Render("╯"),
	)

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	return s.String()
}

func (o *Overlay) Clear() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	var s strings.Builder
	s.WriteString("\033[?7l")
	s.WriteString("\0337")

	for i := range o.LastWindowSize + 2 {
		s.WriteString("\0338")
		fmt.Fprintf(&s, "\033[%dB", i+1)
		s.WriteString("\r\033[2K")
	}

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
		s.WriteString(strings.Repeat(" ", o.LastGhostLen+10))
		s.WriteString("\0338")
		o.LastGhostLen = 0
	}

	s.WriteString("\0337")

	for i := range o.LastWindowSize + 2 {
		s.WriteString("\0338")
		fmt.Fprintf(&s, "\033[%dB", i+1)
		s.WriteString("\r\033[2K")
	}

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
		s.WriteString(strings.Repeat(" ", o.LastGhostLen+10))
		s.WriteString("\0338")
		o.LastGhostLen = 0
	}

	s.WriteString("\0337")

	for i := range o.LastWindowSize + 2 {
		s.WriteString("\0338")
		fmt.Fprintf(&s, "\033[%dB", i+1)
		s.WriteString("\r\033[2K")
	}

	s.WriteString("\0338")
	s.WriteString("\033[?7h")
	o.LastWindowSize = 0
	return s.String()
}

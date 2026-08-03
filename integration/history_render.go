package integration

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/faustbrian/vuja/internal/config"
)

type historyColumns struct {
	ShowDuration     bool
	ShowRelativeTime bool
	ShowExitStatus   bool
	ShowCwd          bool
	DurationWidth    int
	RelativeWidth    int
	ExitWidth        int
	CwdWidth         int
	CommandWidth     int
}

func historyColumnLayout(available int, cfg config.HistoryUIConfig) historyColumns {
	columns := historyColumns{
		ShowDuration:     true,
		ShowRelativeTime: true,
		ShowExitStatus:   cfg.ShowExitStatus,
		ShowCwd:          cfg.ShowCwd,
		DurationWidth:    6,
		RelativeWidth:    9,
		ExitWidth:        3,
		CwdWidth:         min(20, max(12, available/4)),
	}

	width := func() int {
		metadata := 0
		count := 1
		if columns.ShowDuration {
			metadata += columns.DurationWidth
			count++
		}
		if columns.ShowRelativeTime {
			metadata += columns.RelativeWidth
			count++
		}
		if columns.ShowExitStatus {
			metadata += columns.ExitWidth
			count++
		}
		if columns.ShowCwd {
			metadata += columns.CwdWidth
			count++
		}
		return available - metadata - (count - 1)
	}

	const minimumCommandWidth = 12
	if columns.ShowCwd && width() < minimumCommandWidth {
		columns.ShowCwd = false
	}
	if columns.ShowExitStatus && width() < minimumCommandWidth {
		columns.ShowExitStatus = false
	}
	if columns.ShowRelativeTime && width() < minimumCommandWidth {
		columns.ShowRelativeTime = false
	}
	if columns.ShowDuration && width() < minimumCommandWidth {
		columns.ShowDuration = false
	}
	columns.CommandWidth = max(width(), 1)
	return columns
}

type historyCommandSegment struct {
	Text    string
	Matched bool
}

func historyCommandSegments(command string, ranges []MatchRange, maxWidth int) []historyCommandSegment {
	if maxWidth <= 0 {
		return nil
	}
	runes := []rune(command)
	matched := make([]bool, len(runes))
	for _, match := range ranges {
		start := max(match.Start, 0)
		end := min(match.End, len(runes))
		for index := start; index < end; index++ {
			matched[index] = true
		}
	}

	var segments []historyCommandSegment
	width := 0
	truncated := false
	for index, char := range runes {
		charWidth := lipgloss.Width(string(char))
		if width+charWidth > maxWidth {
			truncated = true
			break
		}
		if len(segments) == 0 || segments[len(segments)-1].Matched != matched[index] {
			segments = append(segments, historyCommandSegment{Matched: matched[index]})
		}
		segments[len(segments)-1].Text += string(char)
		width += charWidth
	}
	if truncated {
		for width >= maxWidth && len(segments) > 0 {
			last := &segments[len(segments)-1]
			lastRunes := []rune(last.Text)
			if len(lastRunes) == 0 {
				segments = segments[:len(segments)-1]
				continue
			}
			removed := lastRunes[len(lastRunes)-1]
			last.Text = string(lastRunes[:len(lastRunes)-1])
			width -= lipgloss.Width(string(removed))
		}
		if len(segments) > 0 && segments[len(segments)-1].Text == "" {
			segments = segments[:len(segments)-1]
		}
		segments = append(segments, historyCommandSegment{Text: "…"})
	}
	return segments
}

func formatHistoryDuration(duration time.Duration) string {
	switch {
	case duration <= 0:
		return ""
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds())
	case duration < time.Minute:
		seconds := duration.Round(100 * time.Millisecond).Seconds()
		if duration%time.Second == 0 {
			return fmt.Sprintf("%ds", int(seconds))
		}
		return fmt.Sprintf("%.1fs", seconds)
	default:
		return duration.Round(time.Second).String()
	}
}

func renderRichHistoryRow(
	result RichHistoryResult,
	selected bool,
	available int,
	theme Theme,
	cfg config.HistoryUIConfig,
) string {
	columns := historyColumnLayout(available, cfg)
	background := lipgloss.NewStyle()
	textStyle := lipgloss.NewStyle().Foreground(theme.Text)
	matchStyle := lipgloss.NewStyle().Foreground(theme.Match).Bold(true)
	metadataStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	if selected {
		background = background.Background(theme.SelBg)
		textStyle = textStyle.Foreground(theme.TextSel).Background(theme.SelBg)
		matchStyle = matchStyle.Background(theme.SelBg)
		metadataStyle = metadataStyle.Foreground(theme.DescSel).Background(theme.SelBg)
	}

	var row strings.Builder
	appendColumn := func(value string, width int) {
		if row.Len() > 0 {
			row.WriteString(background.Render(" "))
		}
		row.WriteString(metadataStyle.Render(fixedWidth(value, width)))
	}
	if columns.ShowDuration {
		appendColumn(formatHistoryDuration(result.Duration), columns.DurationWidth)
	}
	if columns.ShowRelativeTime {
		appendColumn(result.RelativeTime, columns.RelativeWidth)
	}
	if columns.ShowExitStatus {
		exit := ""
		if result.HasExitCode {
			exit = fmt.Sprintf("%d", result.ExitCode)
		}
		appendColumn(exit, columns.ExitWidth)
	}
	if columns.ShowCwd {
		appendColumn(result.Cwd, columns.CwdWidth)
	}
	if row.Len() > 0 {
		row.WriteString(background.Render(" "))
	}

	segments := historyCommandSegments(result.Command, result.MatchRanges, columns.CommandWidth)
	commandWidth := 0
	for _, segment := range segments {
		style := textStyle
		if segment.Matched {
			style = matchStyle
		}
		row.WriteString(style.Render(segment.Text))
		commandWidth += lipgloss.Width(segment.Text)
	}
	if commandWidth < columns.CommandWidth {
		row.WriteString(background.Render(strings.Repeat(" ", columns.CommandWidth-commandWidth)))
	}
	return row.String()
}

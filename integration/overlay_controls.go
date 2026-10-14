package integration

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/spec"
)

func (o *Overlay) SelectedSuggestion() (spec.Suggestion, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.Visible || o.Cursor < 0 || o.Cursor >= len(o.Items) {
		return spec.Suggestion{}, false
	}
	return o.Items[o.Cursor], true
}

func (o *Overlay) ToggleRankingExplanation() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.controlNotice = ""
	o.explainRanking = !o.explainRanking
}

func (o *Overlay) SetControlNotice(notice string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.controlNotice = safeControlText(notice)
}

// RefreshPreferences never sorts history or mutates the shell input. A removed
// row loses selection under the same identity rule as asynchronous enrichment.
func (o *Overlay) RefreshPreferences(blocked func(string) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id, command, navigated := "", "", o.UserNavigated
	if o.Cursor >= 0 && o.Cursor < len(o.Items) {
		id, command = o.Items[o.Cursor].ID, o.Items[o.Cursor].Cmd
	}
	items := make([]spec.Suggestion, 0, len(o.Items))
	var history []RichHistoryResult
	for i, item := range o.Items {
		if blocked(item.Cmd) {
			continue
		}
		item.RankingExplanation = ""
		items = append(items, item)
		if o.RichHistory && i < len(o.HistoryResults) {
			history = append(history, o.HistoryResults[i])
		}
	}
	o.Items, o.HistoryResults = items, history
	o.Cursor = selectedSuggestionIndex(items, id, command)
	o.UserNavigated = navigated && o.Cursor >= 0
	o.Visible = o.Visible && (len(items) > 0 || o.RichHistory)
	o.StartIdx = 0
}

func safeControlText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}

// Called under the overlay lock. Preserve Enter's execution target even when
// space is too narrow for all optional hints or the explanation.
func (o *Overlay) renderControlFooter(width int, classic bool) string {
	selected := o.Cursor >= 0 && o.Cursor < len(o.Items)
	if classic && !selected && !o.explainRanking {
		return ""
	}
	keys := config.Get().Keybindings
	run := " Run typed"
	if selected {
		run = " Run selected"
	}
	hints := []string{o.footerKeyStyle.Render("<Enter>") + o.footerTextStyle.Render(run)}
	if o.explainRanking {
		why := "Select a row to inspect its ranking"
		if selected {
			why = o.Items[o.Cursor].RankingExplanation
			if why == "" {
				why = "No recorded ranking evidence"
				if o.RecentHistory {
					why = "Recent execution order, not a ranking score"
				} else if o.RichHistory {
					why = "History search, not the combined suggestion score"
				}
			}
		}
		hints = append(hints, o.footerTextStyle.Render(safeControlText(why)))
	} else {
		appendHint := func(binding, action string) {
			if label := keybindingLabel(binding); label != "" {
				hint := o.footerKeyStyle.Render(label) + o.footerTextStyle.Render(" "+action)
				candidate := strings.Join(append(slices.Clone(hints), hint), " • ")
				if lipgloss.Width(candidate) <= width {
					hints = append(hints, hint)
				}
			}
		}
		if selected {
			pin := "Pin"
			if slices.ContainsFunc(config.Get().Suggestions.Pins, func(pin string) bool {
				return strings.TrimSpace(pin) == strings.TrimSpace(o.Items[o.Cursor].Cmd)
			}) {
				pin = "Unpin"
			}
			appendHint(keys.SuggestionPin, pin)
			appendHint(keys.SuggestionBlock, "Block")
			appendHint(keys.SuggestionExplain, "Why")
		}
		accept := "Select"
		if len(o.Items) == 1 {
			accept = "Accept"
		} else if selected {
			accept = "Next"
		}
		appendHint(keys.Accept, accept)
		appendHint(keys.HistorySearch, "Mode")
	}
	return " " + ansi.Truncate(strings.Join(hints, o.footerTextStyle.Render(" • ")), max(width, 0), "…") + " "
}

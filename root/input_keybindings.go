package root

import (
	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
)

type inputAction string

const (
	inputHistorySearch      inputAction = "history-search"
	inputHistorySuccessOnly inputAction = "history-success-only"
	inputAccept             inputAction = "accept"
	inputToggleOverlay      inputAction = "toggle-overlay"
	inputAcceptToken        inputAction = "accept-token"
	inputMoveBeginning      inputAction = "move-beginning"
	inputMoveEnd            inputAction = "move-end"
	inputClearScreen        inputAction = "clear-screen"
	inputClearLine          inputAction = "clear-line"
	inputCancel             inputAction = "cancel"
	inputDeleteWord         inputAction = "delete-word"
	inputNavigationUp       inputAction = "navigation-up"
	inputNavigationDown     inputAction = "navigation-down"
	inputNavigationPageUp   inputAction = "navigation-page-up"
	inputNavigationPageDown inputAction = "navigation-page-down"
	inputNavigationFirst    inputAction = "navigation-first"
	inputNavigationLast     inputAction = "navigation-last"
)

type inputKeybindings map[inputAction][][]byte

func newInputKeybindings(bindings config.KeybindingsConfig) inputKeybindings {
	result := make(inputKeybindings, 17)
	add := func(action inputAction, values ...string) {
		for _, value := range values {
			sequences, err := config.KeySequences(value)
			if err == nil {
				result[action] = append(result[action], sequences...)
			}
		}
	}

	add(inputHistorySearch, bindings.HistorySearch)
	add(inputHistorySuccessOnly, bindings.HistorySuccessOnly)
	add(inputAccept, bindings.Accept)
	add(inputToggleOverlay, bindings.ToggleOverlay)
	add(inputAcceptToken, bindings.AcceptToken...)
	line := bindings.ResolvedLineEditingBindings()
	add(inputMoveBeginning, line["move-beginning"])
	add(inputMoveEnd, line["move-end"])
	add(inputClearScreen, line["clear-screen"])
	add(inputClearLine, line["clear-line"])
	add(inputCancel, line["cancel"])
	add(inputDeleteWord, line["delete-word"])
	add(inputNavigationUp, bindings.NavigationUp)
	add(inputNavigationDown, bindings.NavigationDown)
	add(inputNavigationPageUp, bindings.NavigationPageUp)
	add(inputNavigationPageDown, bindings.NavigationPageDown)
	add(inputNavigationFirst, bindings.NavigationFirst)
	add(inputNavigationLast, bindings.NavigationLast)

	return result
}

var navigationInputActions = []inputAction{
	inputNavigationUp,
	inputNavigationDown,
	inputNavigationPageUp,
	inputNavigationPageDown,
	inputNavigationFirst,
	inputNavigationLast,
}

func (bindings inputKeybindings) navigationRun(data []byte, index int) ([]inputAction, int) {
	var actions []inputAction
	consumed := 0
	for index+consumed < len(data) {
		matched := false
		for _, action := range navigationInputActions {
			length := bindings.match(action, data, index+consumed)
			if length == 0 {
				continue
			}
			actions = append(actions, action)
			consumed += length
			matched = true
			break
		}
		if !matched {
			break
		}
	}
	return actions, consumed
}

func overlayNavigationActions(actions []inputAction) []integration.NavigationAction {
	result := make([]integration.NavigationAction, 0, len(actions))
	for _, action := range actions {
		switch action {
		case inputNavigationUp:
			result = append(result, integration.NavigationUp)
		case inputNavigationDown:
			result = append(result, integration.NavigationDown)
		case inputNavigationPageUp:
			result = append(result, integration.NavigationPageUp)
		case inputNavigationPageDown:
			result = append(result, integration.NavigationPageDown)
		case inputNavigationFirst:
			result = append(result, integration.NavigationFirst)
		case inputNavigationLast:
			result = append(result, integration.NavigationLast)
		}
	}
	return result
}

func (bindings inputKeybindings) match(action inputAction, data []byte, index int) int {
	return config.KeySequenceMatches(data, index, bindings[action])
}

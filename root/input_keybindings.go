package root

import "github.com/faustbrian/vuja/internal/config"

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
)

type inputKeybindings map[inputAction][][]byte

func newInputKeybindings(bindings config.KeybindingsConfig) inputKeybindings {
	result := make(inputKeybindings, 11)
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

	return result
}

func (bindings inputKeybindings) match(action inputAction, data []byte, index int) int {
	return config.KeySequenceMatches(data, index, bindings[action])
}

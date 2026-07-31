package root

import "github.com/faustbrian/vuja/internal/config"

type inputAction string

const (
	inputHistorySearch      inputAction = "history-search"
	inputHistorySuccessOnly inputAction = "history-success-only"
	inputAccept             inputAction = "accept"
	inputToggleOverlay      inputAction = "toggle-overlay"
	inputAcceptToken        inputAction = "accept-token"
)

type inputKeybindings map[inputAction][][]byte

func newInputKeybindings(bindings config.KeybindingsConfig) inputKeybindings {
	result := make(inputKeybindings, 5)
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

	return result
}

func (bindings inputKeybindings) match(action inputAction, data []byte, index int) int {
	return config.KeySequenceMatches(data, index, bindings[action])
}

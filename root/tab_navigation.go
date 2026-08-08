package root

import "time"

const doubleTabWindow = 300 * time.Millisecond

type tabAction string

const (
	tabNoop          tabAction = "noop"
	tabCycleNext     tabAction = "cycle-next"
	tabAcceptFirst   tabAction = "accept-first"
	tabAcceptCurrent tabAction = "accept-current"
)

type tabNavigation struct {
	active        bool
	doublePending bool
	firstPressed  time.Time
}

func (navigation *tabNavigation) press(now time.Time, suggestionCount int) tabAction {
	if suggestionCount <= 0 {
		navigation.reset()
		return tabNoop
	}
	if suggestionCount == 1 {
		navigation.reset()
		return tabAcceptCurrent
	}
	if !navigation.active {
		navigation.active = true
		navigation.doublePending = true
		navigation.firstPressed = now
		return tabCycleNext
	}
	if navigation.doublePending && now.Sub(navigation.firstPressed) <= doubleTabWindow {
		navigation.reset()
		return tabAcceptFirst
	}

	navigation.doublePending = false
	return tabCycleNext
}

func (navigation *tabNavigation) reset() {
	*navigation = tabNavigation{}
}

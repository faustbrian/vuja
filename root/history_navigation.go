package root

import "sync"

type historyNavigation struct {
	mu       sync.Mutex
	active   bool
	accepted bool
	original string
	selected string
}

func (n *historyNavigation) Begin(original string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.active = true
	n.accepted = false
	n.original = original
	n.selected = ""
}

func (n *historyNavigation) Select(command string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active {
		n.selected = command
	}
}

func (n *historyNavigation) PendingReplacement() (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.accepted {
		return "", false
	}
	return n.selected, n.selected != ""
}

func (n *historyNavigation) Accept() (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	selected := n.selected
	ok := n.active && selected != ""
	n.active = false
	n.accepted = ok
	n.original = ""
	return selected, ok
}

func (n *historyNavigation) Cancel() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	original := n.original
	n.active = false
	n.accepted = false
	n.original = ""
	n.selected = ""
	return original
}

func (n *historyNavigation) Active() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active
}

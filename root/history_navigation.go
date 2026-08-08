package root

import "sync"

type historyNavigation struct {
	mu       sync.Mutex
	active   bool
	accepted bool
	original string
	selected string
	mirrored bool
}

func (n *historyNavigation) Begin(original string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.active = true
	n.accepted = false
	n.original = original
	n.selected = ""
	n.mirrored = false
}

func (n *historyNavigation) BeginMirrored(original string) {
	n.Begin(original)
	n.mu.Lock()
	n.mirrored = true
	n.mu.Unlock()
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
	n.mirrored = false
	return original
}

func (n *historyNavigation) Mirrored() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active && n.mirrored
}

func (n *historyNavigation) Active() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active
}

func historyPromptReplacement(command string) []byte {
	if command == "" {
		return nil
	}
	return append([]byte{0x15}, command...)
}

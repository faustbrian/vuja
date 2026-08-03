package root

import (
	"sync"
	"unicode/utf8"

	"github.com/faustbrian/vuja/integration"
)

type historyScope = integration.HistoryScope

const (
	historyScopeDirectory = integration.HistoryScopeDirectory
	historyScopeProject   = integration.HistoryScopeProject
	historyScopeGlobal    = integration.HistoryScopeGlobal
	historyScopeMachine   = integration.HistoryScopeMachine
	historyScopeSession   = integration.HistoryScopeSession
)

type historySearchState struct {
	Active         bool
	Original       string
	Query          string
	Scope          historyScope
	SuccessfulOnly bool
}

type historySearchSession struct {
	mu    sync.Mutex
	state historySearchState
}

func (s *historySearchSession) Open(original string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = historySearchState{
		Active:   true,
		Original: original,
		Scope:    historyScopeDirectory,
	}
}

func (s *historySearchSession) Append(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Active {
		s.state.Query += value
	}
}

func (s *historySearchSession) Backspace() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Active || s.state.Query == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(s.state.Query)
	s.state.Query = s.state.Query[:len(s.state.Query)-size]
}

func (s *historySearchSession) CycleScope() {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state.Scope {
	case historyScopeDirectory:
		s.state.Scope = historyScopeProject
	case historyScopeProject:
		s.state.Scope = historyScopeGlobal
	case historyScopeGlobal:
		s.state.Scope = historyScopeMachine
	case historyScopeMachine:
		s.state.Scope = historyScopeSession
	default:
		s.state.Scope = historyScopeDirectory
	}
}

func (s *historySearchSession) ToggleSuccessfulOnly() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.SuccessfulOnly = !s.state.SuccessfulOnly
}

func (s *historySearchSession) Close() historySearchState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	s.state = historySearchState{}
	return state
}

func (s *historySearchSession) State() historySearchState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *historySearchSession) Active() bool {
	return s.State().Active
}

func (s historySearchState) Label() string {
	label := string(s.Scope)
	if s.SuccessfulOnly {
		label += " success"
	}
	return label
}

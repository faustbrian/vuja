package root

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
)

type suggestionFeedbackSession struct {
	mu       sync.Mutex
	offered  string
	accepted string
}

type suggestionFeedbackEvent struct {
	command string
	kind    string
}

func (s *suggestionFeedbackSession) offer(command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accepted == "" {
		s.offered = strings.TrimSpace(command)
	}
}

func (s *suggestionFeedbackSession) accept(command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = strings.TrimSpace(command)
}

func (s *suggestionFeedbackSession) finish(submitted string) []suggestionFeedbackEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	submitted = strings.TrimSpace(submitted)
	var events []suggestionFeedbackEvent
	if s.accepted == "" {
		events = append(events, suggestionFeedbackEvent{submitted, "typed"})
	} else if submitted == s.accepted {
		events = append(events, suggestionFeedbackEvent{s.accepted, "accepted"})
	} else {
		events = append(events, suggestionFeedbackEvent{s.accepted, "edited"})
	}
	if s.offered != "" && s.offered != submitted && s.offered != s.accepted {
		events = append(events, suggestionFeedbackEvent{s.offered, "dismissed"})
	}
	s.offered, s.accepted = "", ""
	return events
}

func (s *suggestionFeedbackSession) reset() {
	s.mu.Lock()
	s.offered, s.accepted = "", ""
	s.mu.Unlock()
}

func recordSuggestionFeedback(events []suggestionFeedbackEvent, cwd string) {
	if len(events) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		store, err := scoring.GetFrecencyStore()
		if err != nil {
			return
		}
		for _, event := range events {
			if policy.IsSensitive(event.command) {
				continue
			}
			_ = store.RecordFeedback(ctx, event.command, cwd, event.kind)
		}
	}()
}

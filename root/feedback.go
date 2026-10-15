package root

import (
	"context"
	"errors"
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

// Freeze interaction evidence on Enter, but classify it only against the
// command the shell accepted. Later asynchronous offers cannot alter it.
type suggestionFeedbackSubmission struct {
	offered  string
	accepted string
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

func (s *suggestionFeedbackSession) capture() suggestionFeedbackSubmission {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := suggestionFeedbackSubmission{offered: s.offered, accepted: s.accepted}
	s.offered, s.accepted = "", ""
	return result
}

func (s suggestionFeedbackSubmission) finish(submitted string) []suggestionFeedbackEvent {
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
	return events
}

func (s *suggestionFeedbackSession) reset() {
	s.mu.Lock()
	s.offered, s.accepted = "", ""
	s.mu.Unlock()
}

func recordSuggestionFeedback(events []suggestionFeedbackEvent, cwd, eventKey string) {
	if len(events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	recordSuggestionFeedbackToStore(ctx, scoring.LoadedFrecencyStore(), events, cwd, eventKey)
}

func recordSuggestionFeedbackToStore(
	ctx context.Context,
	store *scoring.FrecencyStore,
	events []suggestionFeedbackEvent,
	cwd string,
	eventKey string,
) {
	if store == nil || len(events) == 0 {
		return
	}
	for _, event := range events {
		if policy.IsSensitive(event.command) {
			continue
		}
		if err := store.RecordExecutionFeedback(ctx, eventKey, event.command, cwd, event.kind); err != nil && !errors.Is(err, scoring.ErrHistoryForgotten) {
			recordHistoryDerivedFailure("feedback", err)
		}
	}
	scoring.InvalidateSignalCache()
}

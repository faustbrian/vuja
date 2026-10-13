package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
)

func TestAIEngineHealthObservesAttemptsButNotThrottleOrCancellation(t *testing.T) {
	var observations []health.Observation
	ctx := health.WithObserver(context.Background(), func(observation health.Observation) { observations = append(observations, observation) })
	fail := true
	engine := NewAIEngine(func(context.Context, string, EnvSnapshot, string) (*spec.Suggestion, error) {
		if fail {
			return nil, errors.New("credential-sentinel private-command /private/path")
		}
		return nil, nil
	})
	engine.Suggest(ctx, "git fixture", EnvSnapshot{}, "fictional context")
	if len(observations) != 1 || observations[0].Outcome != health.Failed {
		t.Fatalf("actual AI failure was discarded: %+v", observations)
	}
	engine.Suggest(ctx, "git fixture", EnvSnapshot{}, "fictional context")
	if len(observations) != 1 {
		t.Fatal("throttle skip falsely reported provider recovery")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	engine.Suggest(cancelled, "git fixture", EnvSnapshot{}, "fictional context")
	if len(observations) != 1 {
		t.Fatal("user cancellation became provider failure")
	}
	engine.mu.Lock()
	engine.lastCallTime = time.Now().Add(-24 * time.Hour)
	engine.mu.Unlock()
	fail = false
	engine.Suggest(ctx, "git fixture", EnvSnapshot{}, "fictional context")
	if len(observations) != 2 || observations[1].Outcome != health.Healthy {
		t.Fatal("actual successful empty response was not distinguished from a skip")
	}
	data, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "credential-sentinel") || strings.Contains(string(data), "private-command") || strings.Contains(string(data), "/private/path") {
		t.Fatal("provider observation retained private error data")
	}
}

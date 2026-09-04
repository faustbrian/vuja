package root

import (
	"testing"
	"time"
)

func TestTabNavigationFocusesCyclesAndAccepts(t *testing.T) {
	start := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	t.Run("single suggestion accepts immediately", func(t *testing.T) {
		var navigation tabNavigation
		if got := navigation.press(start, 1); got != tabAcceptCurrent {
			t.Fatalf("expected immediate acceptance, got %s", got)
		}
	})

	t.Run("first tab cycles and quick second tab accepts first", func(t *testing.T) {
		var navigation tabNavigation
		if got := navigation.press(start, 3); got != tabCycleNext {
			t.Fatalf("expected the first tab to cycle, got %s", got)
		}
		if got := navigation.press(start.Add(150*time.Millisecond), 3); got != tabAcceptFirst {
			t.Fatalf("expected quick double tab to accept first row, got %s", got)
		}
	})

	t.Run("slower tabs cycle without accepting", func(t *testing.T) {
		var navigation tabNavigation
		if got := navigation.press(start, 3); got != tabCycleNext {
			t.Fatalf("expected first tab to cycle, got %s", got)
		}
		if got := navigation.press(start.Add(500*time.Millisecond), 3); got != tabCycleNext {
			t.Fatalf("expected second tab to cycle, got %s", got)
		}
		if got := navigation.press(start.Add(550*time.Millisecond), 3); got != tabCycleNext {
			t.Fatalf("expected navigation to keep cycling, got %s", got)
		}
	})
}

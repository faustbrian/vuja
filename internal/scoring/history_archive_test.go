package scoring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Goal QA scenario 3: native archives merge execution identities, not adapter
// snapshots. Use the real store and observe retained identities and evidence.
type historyArchiveStore interface {
	ImportHistoryArchiveEvents(context.Context, []HistoryEvent) (int, error)
}

func archiveStoreForTest(t *testing.T, store *FrecencyStore) historyArchiveStore {
	t.Helper()
	archive, ok := any(store).(historyArchiveStore)
	if !ok {
		t.Fatal("canonical store has no native archive merge capability")
	}
	return archive
}

func archiveEventForTest(key, command, source string, imported bool, at time.Time) HistoryEvent {
	return HistoryEvent{EventKey: key, Command: command, NormalizedCommand: command, Cwd: "/repo",
		SubmittedAt: at, StartedAt: at, CompletedAt: at.Add(time.Second), Duration: time.Second,
		State: "completed", HasExitCode: true, Source: source, Host: "archive-host", SessionID: "archive-session",
		Shell: "zsh", HistoryOrder: 1, Occurrences: 1, Imported: imported}
}

func TestHistoryArchiveMergePreservesIdentityProvenanceAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := archiveStoreForTest(t, store)
	at := time.Date(2026, 10, 1, 12, 0, 0, 123, time.UTC)
	first := archiveEventForTest("vuja:first", "git status", "vuja", false, at)
	second := archiveEventForTest("vuja:second", "git status", "vuja", false, at.Add(time.Hour))
	second.HistoryOrder = 2
	external := archiveEventForTest("atuin:one", "ssh forge@archive", "atuin", true, at)
	external.Occurrences = 3
	input := []HistoryEvent{first, second, external, first}
	if added, err := archive.ImportHistoryArchiveEvents(t.Context(), input); err != nil || added != 3 {
		t.Fatalf("merge distinct identities: added=%d error=%v", added, err)
	}
	if added, err := archive.ImportHistoryArchiveEvents(t.Context(), input); err != nil || added != 0 {
		t.Fatalf("duplicate archive changed history: added=%d error=%v", added, err)
	}
	if rows, err := store.QueryLocal(t.Context(), "/repo", "git status", 10); err != nil || len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("native ranking evidence was not reconstructed exactly once: %+v %v", rows, err)
	}
	if rows, err := store.QueryLocal(t.Context(), "/repo", external.Command, 10); err != nil || len(rows) != 1 || rows[0].Count != 3 {
		t.Fatalf("imported aggregate evidence was not preserved: %+v %v", rows, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 3 {
		t.Fatalf("reopened archive: %+v %v", events, err)
	}
	byID := make(map[string]HistoryEvent)
	for _, event := range events {
		byID[event.EventKey] = event
	}
	for _, want := range []HistoryEvent{first, second, external} {
		got := byID[want.EventKey]
		if got.Command != want.Command || got.Source != want.Source || got.Imported != want.Imported || got.Occurrences != want.Occurrences ||
			!got.StartedAt.Equal(want.StartedAt) || got.Host != want.Host || got.SessionID != want.SessionID || got.Shell != want.Shell || got.Duration != want.Duration {
			t.Fatalf("execution/provenance changed on archive round trip: got %+v want %+v", got, want)
		}
	}
}

func TestHistoryArchivePortableOrderMatchesNativeAllocation(t *testing.T) {
	for _, order := range []int{1<<53 - 2, 1<<53 - 1, 1 << 53, math.MaxInt64} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			event := archiveEventForTest("vuja:high-order", "git status", "vuja", false, at)
			event.HistoryOrder = order
			if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 1 {
				t.Fatalf("portable native ordinal was rejected: added=%d error=%v", added, err)
			}
			for index := range 2 {
				next := archiveEventForTest(fmt.Sprint("vuja:next-", index), "pwd", "vuja", false, at.Add(time.Duration(index+1)*time.Hour))
				next.HistoryOrder = 0
				err := store.RecordHistorySubmission(t.Context(), next)
				if order == math.MaxInt64 {
					if err == nil || !strings.Contains(err.Error(), "exhausted") {
						t.Fatalf("exhausted native sequence was not rejected explicitly: %v", err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.CompleteHistoryEvent(t.Context(), next); err != nil {
					t.Fatal(err)
				}
			}
			events, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 3
			if order == math.MaxInt64 {
				wantCount = 1
			}
			if len(events) != wantCount {
				t.Fatalf("native admission changed an unexpected number of records: %d", len(events))
			}
			portable, err := PrepareHistoryArchiveEvents(events)
			if err != nil {
				t.Fatalf("supported native allocation made stored history unexportable: %v", err)
			}
			for _, got := range portable {
				if got.EventKey == event.EventKey && got.HistoryOrder != order {
					t.Fatalf("portable ordinal lost precision: got %d want %d", got.HistoryOrder, order)
				}
			}
		})
	}
}

func TestHistoryArchiveRejectsConflictsAndUnsafeInputAtomically(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"existing ID", "duplicate ID", "private command", "ignored command", "private normalized command", "unsafe directory", "invalid state", "negative duration", "native Atuin negative duration", "invalid Atuin negative duration", "missing ID", "missing submission order", "existing submission order", "duplicate submission order"} {
		t.Run(kind, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			archive := archiveStoreForTest(t, store)
			original := archiveEventForTest("vuja:existing", "git status", "vuja", false, at)
			if err := store.RecordHistoryEvent(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			fresh := archiveEventForTest("vuja:fresh", "git log", "vuja", false, at.Add(time.Hour))
			fresh.HistoryOrder = 2
			bad := archiveEventForTest("vuja:invalid", "pwd", "vuja", false, at)
			bad.HistoryOrder = 3
			switch kind {
			case "existing ID":
				bad.EventKey = original.EventKey
			case "duplicate ID":
				bad.EventKey = fresh.EventKey
			case "private command":
				bad.Command = "curl --token private-fixture https://example.test"
			case "ignored command":
				bad.Command = " pwd"
			case "private normalized command":
				bad.NormalizedCommand = "curl --token private-fixture https://example.test"
			case "unsafe directory":
				bad.Cwd = "/repo\x1b]52;c;clipboard\a"
			case "invalid state":
				bad.State = "invented"
			case "negative duration":
				bad.Duration = -time.Second
			case "native Atuin negative duration":
				bad.Source, bad.Duration = "atuin", -1
			case "invalid Atuin negative duration":
				bad.Source, bad.Duration, bad.Imported = "atuin", -2, true
			case "missing ID":
				bad.EventKey = ""
			case "missing submission order":
				bad.HistoryOrder = 0
			case "existing submission order":
				bad.HistoryOrder = 1
			case "duplicate submission order":
				bad.HistoryOrder = fresh.HistoryOrder
			}
			before, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{fresh, bad}); err == nil || added != 0 {
				t.Fatalf("invalid archive partially admitted: added=%d error=%v", added, err)
			}
			rows, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(rows) != 1 || rows[0].EventKey != original.EventKey || rows[0].Command != original.Command {
				t.Fatalf("failed archive changed canonical state: %+v %v", rows, err)
			}
			after, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil || after.Latest != before.Latest {
				t.Fatal("failed archive published a partial generation")
			}
		})
	}
}

// QA3: accepted portable outcomes must remain unchanged across startup
// normalization. Unknown/no-outcome is valid; unknown/observed-outcome is not.
func TestHistoryArchiveUnknownOutcomeRemainsStableAcrossReopen(t *testing.T) {
	for _, imported := range []bool{false, true} {
		for _, outcome := range []string{"unknown", "success", "failure"} {
			t.Run(fmt.Sprintf("imported=%t/%s", imported, outcome), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "history.db")
				store, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				event := archiveEventForTest("archive:unknown", "git status", "zsh", imported, at)
				event.State, event.HasExitCode = "unknown", outcome != "unknown"
				if outcome == "failure" {
					event.ExitCode = 2
				}
				added, importErr := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event})
				if outcome != "unknown" {
					if importErr == nil || added != 0 {
						t.Fatalf("inconsistent portable outcome admitted: added=%d error=%v", added, importErr)
					}
				} else if importErr != nil || added != 1 {
					t.Fatalf("unknown outcome rejected: added=%d error=%v", added, importErr)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				wantRecords := 0
				if outcome == "unknown" {
					wantRecords = 1
				}
				events, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != wantRecords {
					t.Fatalf("inconsistent outcome persisted or valid one lost: %+v %v", events, err)
				}
				if outcome == "unknown" {
					if events[0] != event {
						t.Fatalf("unknown state/timing changed on reopen: got %+v want %+v", events[0], event)
					}
					if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 0 {
						t.Fatalf("identical reopened archive retry conflicted: added=%d error=%v", added, err)
					}
				}
			})
		}
	}
}

func TestHistoryArchiveTimestampRepresentationIsStable(t *testing.T) {
	for _, field := range []string{"submitted", "started", "completed"} {
		for _, fixture := range []struct {
			name  string
			at    time.Time
			valid bool
		}{
			{"unknown", time.Time{}, true},
			{"before-epoch", time.Unix(-1, 0).UTC(), false},
			{"epoch", time.Unix(0, 0).UTC(), false},
			{"after-epoch", time.Unix(0, 1).UTC(), true},
			{"far-future", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true},
			{"out-of-range", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		} {
			t.Run(field+"/"+fixture.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "history.db")
				store, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				event := archiveEventForTest("zsh:timestamp", "pwd", "zsh", true, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
				event.State, event.HasExitCode = "unknown", false
				switch field {
				case "submitted":
					event.SubmittedAt = fixture.at
				case "started":
					event.StartedAt = fixture.at
				case "completed":
					event.CompletedAt = fixture.at
				}
				want := event
				if want.SubmittedAt.IsZero() {
					want.SubmittedAt = want.StartedAt
				}
				added, importErr := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event})
				if !fixture.valid {
					if importErr == nil || added != 0 {
						t.Fatalf("unrepresentable timestamp admitted: added=%d error=%v", added, importErr)
					}
				} else if importErr != nil || added != 1 {
					t.Fatalf("representable timestamp rejected: added=%d error=%v", added, importErr)
				}
				for iteration := range 2 {
					if iteration > 0 {
						if err := store.Close(); err != nil {
							t.Fatal(err)
						}
						store, err = NewFrecencyStore(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					events, err := store.QueryAllHistoryEvents(t.Context())
					if !fixture.valid {
						if err != nil || len(events) != 0 {
							t.Fatalf("rejected timestamp changed history: %+v %v", events, err)
						}
						continue
					}
					if err != nil || len(events) != 1 || events[0] != want {
						t.Fatalf("accepted timestamp changed on query/reopen: got %+v want %+v error=%v", events, want, err)
					}
					if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 0 {
						t.Fatalf("identical timestamp retry conflicted: added=%d error=%v", added, err)
					}
				}
			})
		}
	}
}

func TestHistoryArchiveSurvivesOptionalAdapterReplacement(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := archiveStoreForTest(t, store)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	external := archiveEventForTest("atuin:owned", "ssh forge@archive", "atuin", true, at)
	if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{external}); err != nil || added != 1 {
		t.Fatalf("seed archive: %d %v", added, err)
	}
	adapter := archiveEventForTest("zsh:replaceable", "git log", "zsh", true, at.Add(time.Hour))
	if err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{adapter}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAdapterHistoryEvents(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != external.EventKey || !events[0].Imported || events[0].Source != "atuin" {
		t.Fatalf("adapter refresh erased or promoted archive provenance: %+v %v", events, err)
	}
}

func TestHistoryArchiveReservesOneAdapterCounterpartWithoutLosingRepeats(t *testing.T) {
	for _, sources := range [][2]string{{"atuin", "zsh"}, {"zsh", "atuin"}, {"atuin", "atuin"}} {
		t.Run(sources[0]+"/"+sources[1], func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			archive := archiveStoreForTest(t, store)
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			owned := archiveEventForTest("archive:owned", "ssh forge@archive", sources[0], true, at)
			if _, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{owned}); err != nil {
				t.Fatal(err)
			}
			copy := archiveEventForTest("adapter:copy", owned.Command, sources[1], true, at.Add(100*time.Millisecond))
			repeat := archiveEventForTest("adapter:repeat", owned.Command, sources[1], true, at.Add(time.Hour))
			for range 2 {
				if err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{copy, repeat}); err != nil {
					t.Fatal(err)
				}
				rows, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(rows) != 2 {
					t.Fatalf("archive and adapter duplicated an execution or lost an independent repeat: %+v %v", rows, err)
				}
				ids := map[string]bool{}
				for _, row := range rows {
					ids[row.EventKey] = true
				}
				if !ids[owned.EventKey] || !ids[repeat.EventKey] {
					t.Fatalf("archive identity was replaced by an adapter alias: %+v", rows)
				}
			}
		})
	}
}

func TestHistoryArchiveCompactOwnersDoNotClaimIndependentAdapterExecutions(t *testing.T) {
	for _, imported := range []bool{false, true} {
		for _, source := range []string{"atuin", "zsh"} {
			t.Run(fmt.Sprintf("imported=%t/adapter=%s", imported, source), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "history.db")
				store, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				aggregate := archiveEventForTest("archive:compact", "git status", "legacy-vuja", imported, at)
				aggregate.SessionID, aggregate.Occurrences = "legacy-migration", 7
				if imported {
					aggregate.Source = "atuin"
				}
				if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{aggregate}); err != nil || added != 1 {
					t.Fatalf("restore aggregate: added=%d error=%v", added, err)
				}
				// An unrelated imported exclusion exercises retained-origin
				// reservation too: a compact owner must not infer an alias even
				// when it would be non-preferred and leave the input visible.
				forgotten := archiveEventForTest("atuin:forgotten", "printf unrelated", "atuin", true, at.Add(-time.Hour))
				if err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{forgotten}); err != nil {
					t.Fatal(err)
				}
				if removed, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil || removed != 1 {
					t.Fatalf("seed unrelated exclusion: removed=%d error=%v", removed, err)
				}
				adapter := archiveEventForTest("adapter:independent", aggregate.Command, source, true, at.Add(100*time.Millisecond))
				for step := range 3 {
					if step == 2 {
						if err := store.Close(); err != nil {
							t.Fatal(err)
						}
						reopened, err := NewFrecencyStore(path)
						if err != nil {
							t.Fatal(err)
						}
						store = reopened
					}
					if err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{adapter}); err != nil {
						t.Fatalf("independent adapter refresh: %v", err)
					}
					rows, err := store.QueryAllHistoryEvents(t.Context())
					if err != nil || len(rows) != 2 {
						t.Fatalf("compact owner suppressed an independent execution: %+v %v", rows, err)
					}
					byID := map[string]HistoryEvent{}
					for _, row := range rows {
						byID[row.EventKey] = row
					}
					if byID[aggregate.EventKey].Occurrences != 7 || byID[aggregate.EventKey].Imported != imported ||
						byID[adapter.EventKey].Occurrences != 1 || !byID[adapter.EventKey].Imported {
						t.Fatalf("independent identities/counts/provenance changed: %+v", rows)
					}
					frequencies, err := store.QueryLocal(t.Context(), "/repo", aggregate.Command, 10)
					if err != nil || len(frequencies) != 1 || frequencies[0].Count != 8 {
						t.Fatalf("independent frequency evidence lost: %+v %v", frequencies, err)
					}
					var aliases int
					if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM history_counterpart_sources WHERE owner_hash = ?", forgottenHistoryIdentity(aggregate.EventKey)).Scan(&aliases); err != nil || aliases != 0 {
						t.Fatalf("aggregate inferred a single-execution alias: count=%d error=%v", aliases, err)
					}
				}
			})
		}
	}
}

func TestHistoryArchiveForgettingImportedAggregateDoesNotClaimNativeExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	aggregate := archiveEventForTest("archive:compact", "git status", "atuin", true, at)
	// Portable scope can outlive a prior retention decision: one retained
	// occurrence is still not an individually identified execution.
	aggregate.Aggregate = true
	if _, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{aggregate}); err != nil {
		t.Fatal(err)
	}
	native := completedForgetFixture(t, store, "native:independent", aggregate.Command, at.Add(100*time.Millisecond))
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{aggregate.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget imported aggregate only: removed=%d error=%v", removed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{aggregate}); err != nil || added != 0 {
		t.Fatalf("forgotten imported aggregate returned: added=%d error=%v", added, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != native.EventKey {
		t.Fatalf("aggregate forgetting claimed a native execution: %+v %v", events, err)
	}
	var slots, aliases int
	identity := forgottenHistoryIdentity(aggregate.EventKey)
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM forgotten_native_history WHERE identity_hash = ?", identity).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM history_counterpart_sources WHERE owner_hash = ?", identity).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if slots != 0 || aliases != 0 {
		t.Fatalf("aggregate created single-execution evidence: slots=%d aliases=%d", slots, aliases)
	}
}

func TestHistoryArchiveAggregateDoesNotReserveAnotherForgottenExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	aggregate := archiveEventForTest("archive:compact", "git status", "atuin", true, at)
	aggregate.Aggregate, aggregate.Occurrences = true, 1
	aggregate.State, aggregate.ExitCode, aggregate.Duration = "failed", 7, 10*time.Second
	aggregate.CompletedAt = at.Add(aggregate.Duration)
	if _, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{aggregate}); err != nil {
		t.Fatal(err)
	}
	forgotten := archiveEventForTest("atuin:target", aggregate.Command, "atuin", true, at.Add(500*time.Millisecond))
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{forgotten}); err != nil {
		t.Fatal(err)
	}
	// The shell's native completion can be delayed after the optional source
	// has already observed its outcome. Unknown outcome is not extra evidence
	// that the compact representative owns this native identity.
	native := HistoryEvent{EventKey: "native:target", Command: forgotten.Command, Cwd: "/repo",
		Source: "vuja", SessionID: "live", SubmittedAt: at, StartedAt: at}
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget singleton and its pending native counterpart: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != aggregate.EventKey {
		t.Fatalf("compact reservation shielded a forgotten native counterpart: %+v %v", events, err)
	}
	if excluded, err := store.HistoryEventForgotten(t.Context(), native.EventKey); err != nil || !excluded {
		t.Fatalf("forgotten native identity was not fenced: excluded=%t error=%v", excluded, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	native.HasExitCode, native.CompletedAt, native.Duration = true, at.Add(time.Second), time.Second
	if changed, err := store.CompleteHistoryEvent(t.Context(), native); err != nil || changed {
		t.Fatalf("delayed completion resurrected forgotten native evidence: changed=%t error=%v", changed, err)
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{forgotten}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != aggregate.EventKey {
		t.Fatalf("reopen/reimport changed forgotten scope: %+v %v", events, err)
	}
}

func TestHistoryArchiveAggregateDoesNotPreferAnExternalSingleton(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		aggregate bool
		count     int
		want      int
	}{{"individual control", false, 1, 1}, {"compact", true, 7, 2}, {"retained compact", true, 1, 2}} {
		t.Run(fixture.name, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			owner := archiveEventForTest("archive:atuin", "git status", "atuin", true, at)
			owner.Aggregate, owner.Occurrences = fixture.aggregate, fixture.count
			if _, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{owner}); err != nil {
				t.Fatal(err)
			}
			external := archiveEventForTest("zsh:independent", owner.Command, "zsh", true, at.Add(100*time.Millisecond))
			for range 2 {
				if err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{owner, external}); err != nil {
					t.Fatal(err)
				}
				events, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != fixture.want {
					t.Fatalf("external preference inferred an unsupported alias or lost genuine singleton preference: %+v %v", events, err)
				}
			}
		})
	}
}

func TestHistoryArchiveCompactCandidateDoesNotHideAnIndividualCounterpart(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, count := range []int{0, 1, 7} {
			t.Run(fmt.Sprintf("native=%t/compact-count=%d", native, count), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "history.db")
				store, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				owner := archiveEventForTest("owner:individual", "git status", "atuin", true, at)
				candidateSource := "zsh"
				if native {
					owner.Source, owner.Imported = "vuja", false
					candidateSource = "atuin"
					if _, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{owner}); err != nil {
						t.Fatal(err)
					}
				}
				compact := archiveEventForTest("adapter:compact", owner.Command, candidateSource, true, at.Add(100*time.Millisecond))
				compact.Aggregate, compact.Occurrences = true, count
				copy := archiveEventForTest("adapter:copy", owner.Command, candidateSource, true, at.Add(500*time.Millisecond))
				repeat := archiveEventForTest("adapter:repeat", owner.Command, candidateSource, true, at.Add(time.Hour))
				input := []HistoryEvent{copy, repeat}
				wantRows, wantCount := 2, 2
				if count > 0 {
					input = append(input, compact)
					wantRows, wantCount = 3, 2+count
				}
				if !native {
					input = append(input, owner)
				}
				for step := range 3 {
					if step == 2 {
						if err := store.Close(); err != nil {
							t.Fatal(err)
						}
						store, err = NewFrecencyStore(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := store.ReplaceAdapterHistoryEvents(t.Context(), input); err != nil {
						t.Fatal(err)
					}
					rows, err := store.QueryAllHistoryEvents(t.Context())
					if err != nil || len(rows) != wantRows {
						t.Fatalf("compact candidate obstructed singleton deduplication: %+v %v", rows, err)
					}
					ids := map[string]bool{}
					for _, row := range rows {
						ids[row.EventKey] = true
					}
					if !ids[owner.EventKey] || !ids[repeat.EventKey] || ids[copy.EventKey] || count > 0 && !ids[compact.EventKey] {
						t.Fatalf("individual owner, compact evidence, or independent repeat changed: %+v", rows)
					}
					frequencies, err := store.QueryLocal(t.Context(), "/repo", owner.Command, 10)
					if err != nil || len(frequencies) != 1 || frequencies[0].Count != wantCount {
						t.Fatalf("compact candidate duplicated execution frequency: %+v %v", frequencies, err)
					}
					var member string
					if err := store.db.QueryRowContext(t.Context(),
						"SELECT identity_hash FROM history_counterpart_sources WHERE owner_hash = ? AND source_hash = ?",
						forgottenHistoryIdentity(owner.EventKey), historyEvidenceHash("source", candidateSource)).Scan(&member); err != nil || member != forgottenHistoryIdentity(copy.EventKey) {
						t.Fatalf("individual counterpart membership was not preserved: %q %v", member, err)
					}
					var aliases int
					if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM history_counterpart_sources WHERE owner_hash = ? OR identity_hash = ?",
						forgottenHistoryIdentity(compact.EventKey), forgottenHistoryIdentity(compact.EventKey)).Scan(&aliases); err != nil || aliases != 0 {
						t.Fatalf("compact candidate gained an individual alias: count=%d error=%v", aliases, err)
					}
				}
			})
		}
	}
}

func TestHistoryArchiveRetainedSlotCannotShieldCompactForgottenScope(t *testing.T) {
	for _, count := range []int{1, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			owner := completedForgetFixture(t, store, "native:retained", "git status", at)
			forgotten := completedForgetFixture(t, store, "native:forgotten", owner.Command, at.Add(500*time.Millisecond))
			if removed, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil || removed != 1 {
				t.Fatalf("seed exact forgotten execution: removed=%d error=%v", removed, err)
			}
			compact := archiveEventForTest("adapter:compact", owner.Command, "atuin", true, at.Add(100*time.Millisecond))
			compact.Aggregate, compact.Occurrences = true, count
			copy := archiveEventForTest("adapter:individual", owner.Command, "atuin", true, at.Add(950*time.Millisecond))
			for range 2 {
				err := store.ReplaceAdapterHistoryEvents(t.Context(), []HistoryEvent{compact, copy})
				if !errors.Is(err, ErrHistoryCompactCounterpart) {
					t.Fatalf("retained owner masked ambiguous forgotten scope: %v", err)
				}
				rows, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(rows) != 1 || rows[0].EventKey != owner.EventKey {
					t.Fatalf("refused import changed retained history: %+v %v", rows, err)
				}
				var aliases int
				if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM history_counterpart_sources").Scan(&aliases); err != nil || aliases != 0 {
					t.Fatalf("refused import committed counterpart evidence: count=%d error=%v", aliases, err)
				}
			}
		})
	}
}

func TestHistoryArchiveCannotResurrectForgottenNativeOrImportedExecutions(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "imported"}[imported], func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			archive := archiveStoreForTest(t, store)
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			event := archiveEventForTest("event:forgotten", "ssh forge@forgotten", "vuja", imported, at)
			if imported {
				event.Source = "atuin"
			}
			if _, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil {
				t.Fatal(err)
			}
			if removed, err := store.ForgetHistoryEvents(t.Context(), []string{event.EventKey}); err != nil || removed != 1 {
				t.Fatalf("forget archive: %d %v", removed, err)
			}
			if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 0 {
				t.Fatalf("forgotten archive returned: %d %v", added, err)
			}
			if imported {
				event.EventKey = "rotated-source-id"
				if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 0 {
					t.Fatalf("rotated provenance returned: %d %v", added, err)
				}
			}
			if rows, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(rows) != 0 {
				t.Fatalf("forgotten identity persisted: %+v %v", rows, err)
			}
		})
	}
}

func TestHistoryArchiveFailureRollsBackInsertedPrefixAndOwnership(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := archiveStoreForTest(t, store)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	existing := archiveEventForTest("adapter:existing", "pwd", "zsh", true, at)
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{existing}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_archive_fixture BEFORE INSERT ON history_events
WHEN NEW.event_key = 'vuja:reject' BEGIN SELECT RAISE(ABORT, 'archive fixture'); END`); err != nil {
		t.Fatal(err)
	}
	fresh := archiveEventForTest("vuja:fresh", "git log", "vuja", false, at)
	rejected := archiveEventForTest("vuja:reject", "git status", "vuja", false, at)
	rejected.HistoryOrder = 2
	if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{existing, fresh, rejected}); err == nil || added != 0 {
		t.Fatalf("fallible insert prefix was partially committed: %d %v", added, err)
	}
	if rows, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(rows) != 1 || rows[0].EventKey != existing.EventKey {
		t.Fatalf("failed merge left an inserted prefix: %+v %v", rows, err)
	}
	// The failed promotion must not turn the adapter's old row into a durable
	// archive owner either; its next empty source snapshot still removes it.
	if err := store.ReplaceImportedHistoryEvents(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(rows) != 0 {
		t.Fatal("failed merge leaked archive ownership")
	}
}

func TestHistoryArchiveDoesNotOverwriteAnExistingLiveSubmission(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := archiveStoreForTest(t, store)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	event := archiveEventForTest("vuja:live", "git push", "vuja", false, at)
	if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.State, event.HasExitCode, event.CompletedAt, event.Duration = "interrupted", false, time.Time{}, 0
	if added, err := archive.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{event}); err != nil || added != 0 {
		t.Fatalf("identical portable submission conflicted: %d %v", added, err)
	}
	rows, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(rows) != 1 || rows[0].State != "running" || rows[0].HasExitCode {
		t.Fatal("archive rewrote an existing live submission")
	}
}

func TestHistoryArchiveTransitionsFollowExecutionOrderAcrossPartialMerge(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		existing  bool
		clockStep time.Duration
	}{{"reversed archive", false, time.Minute}, {"later execution already retained", true, time.Minute},
		{"equal clocks", true, 0}, {"clock moved backward", true, -time.Minute}} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			first := archiveEventForTest("z:earlier", "git checkout feature", "vuja", false, at)
			second := archiveEventForTest("a:later", "go test ./...", "vuja", false, at.Add(fixture.clockStep))
			second.HistoryOrder = 2
			if fixture.existing {
				if err := store.RecordHistoryEvent(t.Context(), second); err != nil {
					t.Fatal(err)
				}
			}
			assertEdge := func(previous, next string) {
				t.Helper()
				exact, local := store.QueryExactTransitionsWithFallback(t.Context(), previous, "/repo")
				if !local || len(exact) != 1 || exact[0].NextCommand != next || exact[0].Count != 1 {
					t.Fatalf("execution order lost in exact transition %q -> %q: %+v", previous, next, exact)
				}
				skeletons, local := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(previous), "/repo")
				if !local || len(skeletons) != 1 || skeletons[0].NextSkeleton != ExtractSkeleton(next) || skeletons[0].Count != 1 {
					t.Fatalf("execution order lost in skeleton transition: %+v", skeletons)
				}
			}
			for range 2 {
				if _, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{second, first}); err != nil {
					t.Fatal(err)
				}
				assertEdge(first.Command, second.Command)
				if reverse, _ := store.QueryExactTransitionsWithFallback(t.Context(), second.Command, "/repo"); len(reverse) != 0 {
					t.Fatalf("archive synthesized a reverse edge: %+v", reverse)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			assertEdge(first.Command, second.Command)
			third := archiveEventForTest("next:live", "git status", "vuja", false, at.Add(2*time.Minute))
			third.HistoryOrder = 0 // Live admission must allocate after restored history.
			if err := store.RecordHistorySubmission(t.Context(), third); err != nil {
				t.Fatal(err)
			}
			if completed, err := store.CompleteHistoryEvent(t.Context(), third); err != nil || !completed {
				t.Fatalf("complete after native merge: %t %v", completed, err)
			}
			assertEdge(second.Command, third.Command)
			assertEdge(first.Command, second.Command)
		})
	}
}

func TestHistoryArchiveRetainsLatestFailureAcrossEqualCompletionTimes(t *testing.T) {
	for _, latestFails := range []bool{true, false} {
		for _, delayed := range []bool{false, true} {
			t.Run(fmt.Sprintf("latest-fails=%t/delayed=%t", latestFails, delayed), func(t *testing.T) {
				live, err := NewFrecencyStore(filepath.Join(t.TempDir(), "live.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = live.Close() })
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				first := archiveEventForTest("z:first", "git status", "vuja", false, at)
				second := archiveEventForTest("a:second", "go test ./...", "vuja", false, at)
				second.HistoryOrder = 2
				if latestFails {
					second.ExitCode, second.State = 7, "failed"
				} else {
					first.ExitCode, first.State = 7, "failed"
				}
				assertOutcome := func(store *FrecencyStore) {
					t.Helper()
					failure, found := store.QueryRecentFailure(t.Context(), "/repo", 0)
					if found != latestFails || found && (failure.Command != second.Command || failure.ExitCode != second.ExitCode || !failure.FailedAt.Equal(second.CompletedAt)) {
						t.Fatalf("equal completion times changed the latest outcome: failure=%+v found=%t want-failure=%t", failure, found, latestFails)
					}
				}
				for _, event := range []HistoryEvent{first, second} {
					if err := live.RecordHistorySubmission(t.Context(), event); err != nil {
						t.Fatal(err)
					}
					if changed, err := live.CompleteHistoryEvent(t.Context(), event); err != nil || !changed {
						t.Fatalf("live completion: changed=%t error=%v", changed, err)
					}
				}
				assertOutcome(live)
				events, err := live.QueryAllHistoryEvents(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if delayed {
					for index := range events {
						if events[index].EventKey == first.EventKey {
							events[index].State, events[index].HasExitCode, events[index].ExitCode = "interrupted", false, 0
							events[index].CompletedAt, events[index].Duration = time.Time{}, 0
						}
					}
				}
				portable, err := PrepareHistoryArchiveEvents(events)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "restored.db")
				restored, err := NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = restored.Close() })
				if _, err := restored.ImportHistoryArchiveEvents(t.Context(), portable); err != nil {
					t.Fatal(err)
				}
				if delayed {
					if changed, err := restored.CompleteHistoryEvent(t.Context(), first); err != nil || !changed {
						t.Fatalf("delayed completion: changed=%t error=%v", changed, err)
					}
				}
				assertOutcome(restored)
				if err := restored.Close(); err != nil {
					t.Fatal(err)
				}
				restored, err = NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				assertOutcome(restored)
			})
		}
	}
}

func TestHistoryArchiveAggregateScopeDoesNotInventObservedTransitions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := archiveEventForTest("compact:first", "git status", "vuja", false, at)
	second := archiveEventForTest("compact:second", "go test ./...", "vuja", false, at.Add(time.Minute))
	first.Occurrences, second.Occurrences, second.HistoryOrder = 7, 3, 2
	if _, err := store.ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{first, second}); err != nil {
		t.Fatal(err)
	}
	assertNoAggregateEdges := func() {
		t.Helper()
		for _, event := range []HistoryEvent{first, second} {
			exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), event.Command, "/repo")
			skeletons, _ := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(event.Command), "/repo")
			if len(exact) != 0 || len(skeletons) != 0 {
				t.Fatalf("compact native scope became observed adjacency: exact=%+v skeletons=%+v", exact, skeletons)
			}
		}
	}
	assertNoAggregateEdges()
	if _, err := store.PruneHistoryEvents(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	var observed []HistoryEvent
	for index, command := range []string{"npm run build", "npm test"} {
		event := archiveEventForTest(fmt.Sprintf("observed:%d", index), command, "vuja", false, at.Add(time.Duration(index+2)*time.Minute))
		event.HistoryOrder = 0
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		observed = append(observed, event)
	}
	// A late earlier completion exercises the shared repair path as well.
	for _, event := range []HistoryEvent{observed[1], observed[0]} {
		if changed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !changed {
			t.Fatalf("observed completion: changed=%t error=%v", changed, err)
		}
	}
	assertNoAggregateEdges()
	exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), observed[0].Command, "/repo")
	if len(exact) != 1 || exact[0].NextCommand != observed[1].Command || exact[0].Count != 1 {
		t.Fatalf("aggregate exclusion lost genuine observed adjacency: %+v", exact)
	}
}

func TestHistoryArchiveUpgradePreservesOriginalSubmissionOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := archiveEventForTest("z:original-first", "git checkout feature", "vuja", false, at)
	second := archiveEventForTest("a:original-second", "go test ./...", "vuja", false, at)
	for _, event := range []HistoryEvent{first, second} {
		event.HistoryOrder = 0
		if err := store.RecordHistoryEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	// Rehearse the pre-archive schema with native order known only by rowid.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE history_events SET history_order = 0;
DELETE FROM metadata WHERE key IN ('history_native_order_version', 'history_native_sequence');
ALTER TABLE history_events DROP COLUMN archive_owned;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 2 {
		t.Fatalf("upgraded canonical snapshot: %+v %v", events, err)
	}
	byID := make(map[string]HistoryEvent)
	for _, event := range events {
		byID[event.EventKey] = event
	}
	restored, err := NewFrecencyStore(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if _, err := archiveStoreForTest(t, restored).ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{byID[second.EventKey], byID[first.EventKey]}); err != nil {
		t.Fatal(err)
	}
	exact, _ := restored.QueryExactTransitionsWithFallback(t.Context(), first.Command, "/repo")
	if len(exact) != 1 || exact[0].NextCommand != second.Command || exact[0].Count != 1 {
		t.Fatalf("upgrade/restore replaced original submission order with clocks or IDs: %+v", exact)
	}
	if err := restored.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	third := archiveEventForTest("new:after-clear", "pwd", "vuja", false, at)
	third.HistoryOrder = 0
	if err := restored.RecordHistorySubmission(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	rows, err := restored.QueryAllHistoryEvents(t.Context())
	if err != nil || len(rows) != 1 || rows[0].HistoryOrder <= byID[second.EventKey].HistoryOrder {
		t.Fatalf("clearing history reused an archived submission ordinal: %+v %v", rows, err)
	}
}

func TestHistoryArchiveDelayedCompletionRepairsPortableOrder(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := archiveEventForTest("z:first", "git checkout feature", "vuja", false, at)
	middle := archiveEventForTest("m:middle", "go test ./...", "vuja", false, at)
	last := archiveEventForTest("a:last", "npm run build", "vuja", false, at)
	middle.HistoryOrder, last.HistoryOrder = 2, 3
	middle.State, middle.HasExitCode, middle.CompletedAt, middle.Duration = "interrupted", false, time.Time{}, 0
	if _, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{last, middle, first}); err != nil {
		t.Fatal(err)
	}
	middle.HasExitCode, middle.CompletedAt, middle.Duration = true, at.Add(time.Second), time.Second
	if completed, err := store.CompleteHistoryEvent(t.Context(), middle); err != nil || !completed {
		t.Fatalf("delayed canonical completion after restore: %t %v", completed, err)
	}
	for _, pair := range [][2]string{{first.Command, middle.Command}, {middle.Command, last.Command}} {
		exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), pair[0], "/repo")
		if len(exact) != 1 || exact[0].NextCommand != pair[1] || exact[0].Count != 1 {
			t.Fatalf("delayed completion repaired the wrong portable adjacency: %q -> %q: %+v", pair[0], pair[1], exact)
		}
		skeletons, _ := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(pair[0]), "/repo")
		if len(skeletons) != 1 || skeletons[0].NextSkeleton != ExtractSkeleton(pair[1]) || skeletons[0].Count != 1 {
			t.Fatalf("delayed completion repaired the wrong skeleton adjacency: %+v", skeletons)
		}
	}
}

func TestHistoryArchiveRepeatedTransitionKeepsNewestKnownRecency(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	commands := []string{"git status", "go test", "git status", "go test"}
	events := make([]HistoryEvent, 0, len(commands))
	for index, command := range commands {
		event := archiveEventForTest(string(rune('a'+index)), command, "vuja", false, at.Add(time.Duration(index)*10*time.Second))
		event.HistoryOrder = index + 1
		if index == 1 {
			event.CompletedAt = at.Add(time.Hour)
			event.Duration = event.CompletedAt.Sub(event.StartedAt)
		}
		events = append(events, event)
	}
	if _, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), commands[0], "/repo")
	if len(exact) != 1 || exact[0].NextCommand != commands[1] || exact[0].Count != 2 || !exact[0].LastUsed.Equal(events[1].CompletedAt) {
		t.Fatalf("archive rolled repeated exact-transition recency backward: %+v", exact)
	}
	skeletons, _ := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(commands[0]), "/repo")
	if len(skeletons) != 1 || skeletons[0].Count != 2 || !skeletons[0].LastUsed.Equal(events[1].CompletedAt) {
		t.Fatalf("archive rolled repeated skeleton-transition recency backward: %+v", skeletons)
	}
}

func TestHistoryArchiveUnrelatedMergePreservesFailedAttemptRecency(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	commands := []string{"git status", "go test", "git status", "go test"}
	latest := at.Add(time.Hour)
	for index, command := range commands {
		event := archiveEventForTest(string(rune('a'+index)), command, "vuja", false, at.Add(time.Duration(index)*time.Minute))
		event.HistoryOrder = 0
		if index == len(commands)-1 {
			event.State, event.ExitCode, event.CompletedAt = "failed", 1, latest
			event.Duration = latest.Sub(event.StartedAt)
		}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
			t.Fatalf("complete repeated execution: %t %v", completed, err)
		}
	}
	assertEvidence := func() {
		t.Helper()
		exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), commands[0], "/repo")
		if len(exact) != 1 || exact[0].Count != 1 || !exact[0].LastUsed.Equal(latest) {
			t.Fatalf("failed attempt changed retained exact evidence: %+v", exact)
		}
		skeletons, _ := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(commands[0]), "/repo")
		if len(skeletons) != 1 || skeletons[0].Count != 1 || !skeletons[0].LastUsed.Equal(latest) {
			t.Fatalf("failed attempt changed retained skeleton evidence: %+v", skeletons)
		}
	}
	assertEvidence()
	unrelated := archiveEventForTest("atuin:unrelated", "pwd", "atuin", true, at)
	if _, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{unrelated}); err != nil {
		t.Fatal(err)
	}
	assertEvidence()
}

func TestHistoryArchiveOldWriterGetsPortableOrderBeforeNewWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// The pre-update writer supplies native order zero. Use its SQL boundary,
	// not a patched new-writer method that could hide the compatibility race.
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO history_events
(event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
 duration_ns, exit_code, source, session_id, state, history_order, occurrences, imported)
VALUES ('old:first', 'git status', 'git status', '/repo', ?, ?, ?, 0, 0,
 'vuja', 'shared-session', 'completed', 0, 1, 0)`,
		canonicalTimestamp(at), canonicalTimestamp(at), canonicalTimestamp(at)); err != nil {
		t.Fatal(err)
	}
	second := archiveEventForTest("new:second", "go test", "vuja", false, at)
	second.SessionID, second.HistoryOrder = "shared-session", 0
	if err := store.RecordHistorySubmission(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if completed, err := store.CompleteHistoryEvent(t.Context(), second); err != nil || !completed {
		t.Fatalf("complete new execution after old writer: %t %v", completed, err)
	}
	for _, reopen := range []bool{false, true} {
		if reopen {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareHistoryArchiveEvents(events); err != nil {
			t.Fatalf("old/new writers produced an unexportable snapshot (reopen=%t): %v", reopen, err)
		}
		byID := make(map[string]HistoryEvent)
		for _, event := range events {
			byID[event.EventKey] = event
		}
		if byID["old:first"].HistoryOrder <= 0 || byID[second.EventKey].HistoryOrder <= byID["old:first"].HistoryOrder {
			t.Fatalf("mixed writers lost actual submission order: %+v", events)
		}
		exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git status", "/repo")
		if len(exact) != 1 || exact[0].NextCommand != "go test" || exact[0].Count != 1 {
			t.Fatalf("mixed writers lost learned adjacency: %+v", exact)
		}
	}
}

func TestHistoryArchiveRefusesUnattributedLearningWithoutAnyMutation(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "imported"}[imported], func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			first := archiveEventForTest("existing:canonical", "pwd", "vuja", false, at)
			if err := store.RecordHistorySubmission(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if completed, err := store.CompleteHistoryEvent(t.Context(), first); err != nil || !completed {
				t.Fatalf("seed canonical completion: %t %v", completed, err)
			}
			if err := store.RecordTransition(t.Context(), "git checkout", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordExactTransition(t.Context(), "git checkout feature", "npm run dev", "/repo", 0); err != nil {
				t.Fatal(err)
			}
			exactBefore, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonsBefore, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactBefore) != 1 || len(skeletonsBefore) != 1 {
				t.Fatal("compatibility learning was not seeded")
			}
			before, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(before) != 1 {
				t.Fatalf("seed canonical snapshot: %+v %v", before, err)
			}
			changesBefore, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			fresh := archiveEventForTest("new:archive", "git log", "vuja", imported, at.Add(time.Hour))
			fresh.SessionID = "another-session"
			if imported {
				fresh.Source = "atuin"
			}
			if added, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), []HistoryEvent{before[0], fresh}); err == nil || added != 0 || !strings.Contains(err.Error(), "unattributed learning") {
				t.Fatalf("unsafe reconstruction was not refused atomically: added=%d error=%v", added, err)
			}
			exactAfter, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			skeletonsAfter, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
			if len(exactAfter) != 1 || exactAfter[0] != exactBefore[0] || len(skeletonsAfter) != 1 || skeletonsAfter[0] != skeletonsBefore[0] {
				t.Fatalf("refused merge changed learning: exact=%+v skeleton=%+v", exactAfter, skeletonsAfter)
			}
			after, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(after) != 1 || after[0] != before[0] {
				t.Fatalf("refused merge changed canonical executions: %+v %v", after, err)
			}
			var archiveOwned int
			if err := store.db.QueryRowContext(t.Context(), `SELECT SUM(archive_owned) FROM history_events`).Scan(&archiveOwned); err != nil || archiveOwned != 0 {
				t.Fatalf("refused merge promoted archive ownership: %d %v", archiveOwned, err)
			}
			changesAfter, err := store.QueryHistoryChanges(t.Context(), 0, 100)
			if err != nil || changesAfter.Latest != changesBefore.Latest {
				t.Fatal("refused merge published a canonical generation")
			}
			// Equal-ID archives only protect ownership, so unrelated legacy
			// learning must not turn an otherwise identical no-op into a refusal.
			if added, err := archiveStoreForTest(t, store).ImportHistoryArchiveEvents(t.Context(), before); err != nil || added != 0 {
				t.Fatalf("identical archive unnecessarily rebuilt learning: %d %v", added, err)
			}
			exactAfter, _ = store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
			if len(exactAfter) != 1 || exactAfter[0] != exactBefore[0] {
				t.Fatal("identical archive changed compatibility learning")
			}
		})
	}
}

func TestHistoryArchiveLegacyAggregatesDoNotInventTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := canonicalTimestamp(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	commands := []string{"git status", "go test"}
	counts := []int{7, 3}
	for index, command := range commands {
		if _, err := store.db.ExecContext(t.Context(), `INSERT INTO history_entries (cmd, cwd, count, last_used)
VALUES (?, '/repo', ?, ?)`, command, counts[index], at); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(t.Context(), `INSERT INTO command_outcomes (cmd, cwd, successes, failures, last_used)
VALUES (?, '/repo', ?, 0, ?)`, command, counts[index], at); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen the supported pre-lifecycle aggregate boundary. The migration,
	// rather than a hand-written archive fixture, must establish provenance.
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM metadata
WHERE key IN ('history_schema_version', 'history_native_order_version', 'history_native_sequence')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 2 {
		t.Fatalf("migrated aggregate records: %+v %v", events, err)
	}
	for _, event := range events {
		if event.Imported || event.Source != "legacy-vuja" || event.SessionID != "legacy-migration" {
			t.Fatalf("fixture did not exercise native aggregate migration: %+v", event)
		}
	}
	portable, err := PrepareHistoryArchiveEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	restored, err := NewFrecencyStore(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if added, err := archiveStoreForTest(t, restored).ImportHistoryArchiveEvents(t.Context(), portable); err != nil || added != len(events) {
		t.Fatalf("restore migrated aggregate records: %d %v", added, err)
	}
	for index, command := range commands {
		entries, err := restored.QueryLocal(t.Context(), "/repo", command, 10)
		if err != nil || len(entries) != 1 || entries[0].Count != counts[index] {
			t.Fatalf("restore changed legacy frequency: %+v %v", entries, err)
		}
		exact, _ := restored.QueryExactTransitionsWithFallback(t.Context(), command, "/repo")
		skeletons, _ := restored.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(command), "/repo")
		if len(exact) != 0 || len(skeletons) != 0 {
			t.Fatalf("restore invented adjacency for independent aggregates: exact=%+v skeleton=%+v", exact, skeletons)
		}
	}
	retained, err := restored.QueryAllHistoryEvents(t.Context())
	if err != nil || len(retained) != len(events) {
		t.Fatalf("restored aggregate identities: %+v %v", retained, err)
	}
	byID := make(map[string]HistoryEvent)
	for _, event := range retained {
		byID[event.EventKey] = event
	}
	for _, event := range portable {
		if byID[event.EventKey] != event {
			t.Fatalf("restore changed aggregate identity/provenance: got %+v want %+v", byID[event.EventKey], event)
		}
	}
	// Eligibility belongs to the marker pair, not to a session name alone.
	// Mixing real lifecycle rows into that name also challenges predecessor
	// selection and the global repair scan after delayed completion.
	live := make([]HistoryEvent, 0, 3)
	for index, command := range []string{"npm run build", "npm test", "git diff"} {
		event := archiveEventForTest("live:"+string(rune('a'+index)), command, "vuja", false,
			time.Date(2026, 10, 1, 13, index, 0, 0, time.UTC))
		event.SessionID, event.HistoryOrder = "legacy-migration", 0
		if err := restored.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		live = append(live, event)
		if index != 1 {
			if completed, err := restored.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
				t.Fatalf("complete observed lifecycle row: %t %v", completed, err)
			}
		}
	}
	if completed, err := restored.CompleteHistoryEvent(t.Context(), live[1]); err != nil || !completed {
		t.Fatalf("late completion beside synthetic aggregates: %t %v", completed, err)
	}
	assertObservedOnly := func() {
		t.Helper()
		for _, command := range commands {
			exact, _ := restored.QueryExactTransitionsWithFallback(t.Context(), command, "/repo")
			skeletons, _ := restored.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(command), "/repo")
			if len(exact) != 0 || len(skeletons) != 0 {
				t.Fatalf("completion/repair admitted synthetic aggregate evidence: exact=%+v skeleton=%+v", exact, skeletons)
			}
		}
		for index := range 2 {
			exact, _ := restored.QueryExactTransitionsWithFallback(t.Context(), live[index].Command, "/repo")
			if len(exact) != 1 || exact[0].NextCommand != live[index+1].Command || exact[0].Count != 1 {
				t.Fatalf("synthetic-cohort exclusion lost observed adjacency: %+v", exact)
			}
		}
	}
	assertObservedOnly()
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err = NewFrecencyStore(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	assertObservedOnly()
}

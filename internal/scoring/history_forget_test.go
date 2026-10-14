package scoring

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Goal QA scenario 3: observe durable identities and independently expected
// surviving evidence, not SQL statement choreography.
type historyForgetStore interface {
	ForgetHistoryEvents(context.Context, []string) (int64, error)
}

type historyDirectoryStore interface {
	RecordHistoryDirectory(context.Context, string, string) error
}

func TestHistoryForgetImportedPreservesRecentIndependentSameSourceRepeat(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := HistoryEvent{EventKey: "atuin:first", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	kept := forgotten
	kept.EventKey, kept.StartedAt = "atuin:repeat", at.Add(200*time.Millisecond)
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{forgotten, kept}); err != nil {
		t.Fatal(err)
	}
	selected, err := store.QueryHistoryForgetMatches(t.Context(), forgotten.EventKey, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err != nil || removed != 1 {
		t.Fatalf("forget imported target: removed=%d error=%v", removed, err)
	}
	for range 2 {
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil || len(events) != 1 || events[0].EventKey != kept.EventKey {
			t.Fatalf("counterpart exclusion consumed a known independent repeat: %+v %v", events, err)
		}
		if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{forgotten, kept}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryForgetImportedPreservesIndependentCrossSourceEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := HistoryEvent{EventKey: "atuin:first", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, State: "completed", HasExitCode: true}
	kept := forgotten
	kept.EventKey, kept.StartedAt = "atuin:repeat", at.Add(200*time.Millisecond)
	copy := kept
	copy.EventKey, copy.Source, copy.Cwd, copy.HasExitCode = "zsh:repeat", "zsh", "", false
	imports := []HistoryEvent{forgotten, kept, copy}
	if err := store.ReplaceExternalHistoryEvents(t.Context(), imports); err != nil {
		t.Fatal(err)
	}
	selected, err := store.QueryHistoryForgetMatches(t.Context(), forgotten.EventKey, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err != nil || removed != 1 {
		t.Fatalf("forget imported target: removed=%d error=%v", removed, err)
	}
	for range 2 {
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil || len(events) != 2 {
			t.Fatalf("forgotten neighbor consumed independent cross-source evidence: %+v %v", events, err)
		}
		for _, event := range events {
			if event.EventKey != kept.EventKey && event.EventKey != copy.EventKey {
				t.Fatalf("unexpected survivor: %+v", event)
			}
		}
		if err := store.ReplaceExternalHistoryEvents(t.Context(), imports); err != nil {
			t.Fatal(err)
		}
	}
	// A source may disappear from the next snapshot. Its known surviving
	// repetition still constrains deletion of the only remaining copy.
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != copy.EventKey {
		t.Fatalf("partial snapshot lost the retained repetition's sole representation: %+v %v", events, err)
	}
}

func TestHistoryForgetNativeExclusionSurvivesLegacyCounterpartSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	native := HistoryEvent{EventKey: "native:forgotten", Command: "ssh forge@one", Cwd: "/repo", Source: "vuja", StartedAt: at, State: "running"}
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{native.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget native: removed=%d error=%v", removed, err)
	}
	// Reproduce the previous dev schema, retaining its native exclusion.
	if _, err := store.db.ExecContext(t.Context(), `
DROP INDEX idx_forgotten_native_command_time;
ALTER TABLE forgotten_native_history DROP COLUMN native_identity_hash;
ALTER TABLE forgotten_native_history DROP COLUMN forgotten_at;
ALTER TABLE forgotten_native_history DROP COLUMN origin_source_hash;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	copy := native
	copy.EventKey, copy.Source, copy.Imported = "atuin:copy", "atuin", true
	repeat := copy
	repeat.EventKey, repeat.StartedAt = "atuin:repeat", at.Add(time.Hour)
	if err := reopened.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy, repeat}); err != nil {
		t.Fatal(err)
	}
	if events, err := reopened.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != repeat.EventKey {
		t.Fatalf("schema extension changed native exclusion semantics: %+v %v", events, err)
	}
}

func TestHistoryForgetPendingCounterpartPreservesUnrelatedLegacyFeedback(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordFeedback(t.Context(), "git status", "/repo", "dismissed"); err != nil {
		t.Fatal(err)
	}
	pending := HistoryEvent{EventKey: "native:pending", Command: "ssh forge@one", Cwd: "/repo", Source: "vuja", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), State: "running"}
	copy := pending
	copy.EventKey, copy.Source, copy.Imported, copy.State = "atuin:copy", "atuin", true, "completed"
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), []HistoryEvent{pending}, []HistoryEvent{pending}); err != nil || removed != 1 {
		t.Fatalf("pending counterpart removal: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("pending counterpart remains: %+v %v", events, err)
	}
	if feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 10); err != nil || len(feedback) != 1 || feedback[0].Dismissed != 1 {
		t.Fatalf("counterpart projection rebuild erased unrelated legacy learning: %+v %v", feedback, err)
	}
}

func TestHistoryForgetEarlierImportedCounterpartsPreserveIndependentEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	copy := HistoryEvent{EventKey: "atuin:copy", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, StartedAt: at, CompletedAt: at.Add(time.Second), Duration: time.Second, HasExitCode: true, State: "completed", Occurrences: 1}
	imports := []HistoryEvent{copy}
	shellCopy := copy
	shellCopy.EventKey, shellCopy.Source, shellCopy.Cwd, shellCopy.Duration, shellCopy.HasExitCode = "zsh:copy", "zsh", "", 0, false
	imports = append(imports, shellCopy)
	for _, variant := range []struct {
		id     string
		mutate func(*HistoryEvent)
	}{
		{"atuin:near-repeat", func(event *HistoryEvent) { event.StartedAt = at.Add(200 * time.Millisecond) }},
		{"atuin:unknown", func(event *HistoryEvent) { event.StartedAt = time.Time{} }},
		{"atuin:other-cwd", func(event *HistoryEvent) { event.Cwd = "/other" }},
		{"atuin:other-outcome", func(event *HistoryEvent) { event.ExitCode, event.State = 1, "failed" }},
		{"atuin:other-duration", func(event *HistoryEvent) { event.Duration = 5 * time.Second }},
		{"atuin:later", func(event *HistoryEvent) { event.StartedAt = at.Add(time.Hour) }},
	} {
		event := copy
		event.EventKey = variant.id
		variant.mutate(&event)
		imports = append(imports, event)
	}
	if err := store.ReplaceExternalHistoryEvents(t.Context(), imports); err != nil {
		t.Fatal(err)
	}
	native := copy
	native.EventKey, native.Source, native.Imported = "native:delayed", "vuja", false
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	if completed, err := store.CompleteHistoryEvent(t.Context(), native); err != nil || !completed {
		t.Fatalf("completion: completed=%v error=%v", completed, err)
	}
	selected, err := store.QueryHistoryForgetMatches(t.Context(), native.EventKey, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err != nil || removed != 1 {
		t.Fatalf("forget native execution: removed=%d error=%v", removed, err)
	}
	assertSurvivors := func() {
		t.Helper()
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil || len(events) != 6 {
			t.Fatalf("wrong independent survivor count: %+v %v", events, err)
		}
		for _, event := range events {
			if event.EventKey == copy.EventKey || event.EventKey == shellCopy.EventKey || event.EventKey == native.EventKey {
				t.Fatalf("known counterpart survived: %+v", event)
			}
		}
	}
	assertSurvivors()
	if err := store.ReplaceExternalHistoryEvents(t.Context(), imports); err != nil {
		t.Fatal(err)
	}
	assertSurvivors()
}

func TestHistoryForgetConfirmedTargetsBindEveryPreviewedField(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*HistoryEvent)
	}{
		{"command", func(event *HistoryEvent) { event.Command = "git status" }},
		{"directory", func(event *HistoryEvent) { event.Cwd = "/changed" }},
		{"source", func(event *HistoryEvent) { event.Source = "zsh" }},
		{"started", func(event *HistoryEvent) { event.StartedAt = event.StartedAt.Add(time.Nanosecond) }},
		{"occurrences", func(event *HistoryEvent) { event.Occurrences = 2 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			store, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			other, err := NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			original := HistoryEvent{EventKey: "atuin:selected", Command: "ssh forge@one", Cwd: "/repo", Source: "atuin", Imported: true, State: "completed", Occurrences: 7, StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
			if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{original}); err != nil {
				t.Fatal(err)
			}
			selected, err := store.QueryHistoryForgetMatches(t.Context(), original.EventKey, "", "")
			if err != nil || len(selected) != 1 {
				t.Fatalf("preview: %+v %v", selected, err)
			}
			replacement := original
			change.mutate(&replacement)
			if err := other.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{replacement}); err != nil {
				t.Fatal(err)
			}
			if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err == nil || removed != 0 {
				t.Fatalf("changed %s was accepted: removed=%d error=%v", change.name, removed, err)
			}
			current, err := other.QueryHistoryEventsByKeys(t.Context(), []string{replacement.EventKey})
			if err != nil || len(current) != 1 || current[0].Command != replacement.Command || current[0].Cwd != replacement.Cwd || current[0].Source != replacement.Source || !current[0].StartedAt.Equal(replacement.StartedAt) || current[0].Occurrences != replacement.Occurrences {
				t.Fatalf("rejected confirmation changed its replacement: %+v %v", current, err)
			}
			if forgotten, err := other.HistoryEventForgotten(t.Context(), replacement.EventKey); err != nil || forgotten {
				t.Fatalf("rejected confirmation installed an exclusion: %v %v", forgotten, err)
			}
		})
	}
}

func TestHistoryForgetConfirmedTargetsAllowCompletionButRejectMissingCanonical(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := HistoryEvent{EventKey: "completes-after-preview", Command: "git status", Cwd: "/repo", Source: "vuja", StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	selected, err := store.QueryHistoryForgetMatches(t.Context(), event.EventKey, "", "")
	if err != nil || len(selected) != 1 {
		t.Fatalf("preview: %+v %v", selected, err)
	}
	event.CompletedAt, event.Duration, event.HasExitCode, event.ExitCode = event.StartedAt.Add(time.Second), time.Second, true, 1
	if changed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !changed {
		t.Fatalf("late completion: changed=%v error=%v", changed, err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err != nil || removed != 1 {
		t.Fatalf("completion invalidated immutable confirmation: removed=%d error=%v", removed, err)
	}
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err != nil || removed != 0 {
		t.Fatalf("confirmed deletion retry is not idempotent: removed=%d error=%v", removed, err)
	}
	selected[0].EventKey = "absent-unobserved"
	if removed, err := store.ForgetConfirmedHistoryEvents(t.Context(), selected, nil); err == nil || removed != 0 {
		t.Fatalf("missing canonical target became an unobserved exclusion: removed=%d error=%v", removed, err)
	}
	if forgotten, err := store.HistoryEventForgotten(t.Context(), selected[0].EventKey); err != nil || forgotten {
		t.Fatalf("missing target gained an exclusion: %v %v", forgotten, err)
	}
}

func TestHistoryForgetCompoundRequestsDoNotEnterCanonicalImportsOrFeedback(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := HistoryEvent{EventKey: "compound-private", Command: "true; vuja history forget --command 'private-command'", Cwd: "/repo", Source: "vuja", StartedAt: time.Now().UTC()}
	if err := store.RecordHistorySubmission(t.Context(), event); err == nil {
		t.Fatal("canonical submission retained a compound forgetting request")
	}
	if err := store.RecordFeedback(t.Context(), event.Command, event.Cwd, "typed"); err != nil {
		t.Fatal(err)
	}
	event.Source, event.Imported, event.State = "zsh", true, "completed"
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{event}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("external refresh retained the private request: %+v %v", events, err)
	}
	if feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "", 10); err != nil || len(feedback) != 0 {
		t.Fatalf("private request entered feedback: %+v %v", feedback, err)
	}
}

func TestHistoryForgetPendingSelectionPreservesUnrelatedLearningAndRejectsChangedIdentity(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordFeedback(t.Context(), "ssh forge@offered", "/repo", "dismissed"); err != nil {
		t.Fatal(err)
	}
	pending := HistoryEvent{EventKey: "pending-only", Command: "ssh forge@pending", NormalizedCommand: "ssh forge@pending", Cwd: "/repo", Source: "vuja", StartedAt: time.Now().UTC()}
	if removed, err := store.ForgetPendingHistoryEvents(t.Context(), []string{pending.EventKey}, []HistoryEvent{pending}); err != nil || removed != 1 {
		t.Fatalf("pending exclusion: removed=%d error=%v", removed, err)
	}
	feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "ssh", 10)
	if err != nil || len(feedback) != 1 || feedback[0].Dismissed != 1 {
		t.Fatalf("pending-only removal rebuilt unrelated learning: %+v %v", feedback, err)
	}
	if err := store.RecordHistorySubmission(t.Context(), pending); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("pending identity exclusion did not survive recovery: %v", err)
	}
	if removed, err := store.ForgetPendingHistoryEvents(t.Context(), []string{pending.EventKey}, []HistoryEvent{pending}); err != nil || removed != 0 {
		t.Fatalf("retry was not idempotent: removed=%d error=%v", removed, err)
	}
	pending.EventKey = "replayed-since-preview"
	canonical := pending
	canonical.Command, canonical.NormalizedCommand = "git status", "git status"
	if err := store.RecordHistorySubmission(t.Context(), canonical); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetPendingHistoryEvents(t.Context(), []string{pending.EventKey}, []HistoryEvent{pending}); err == nil || removed != 0 {
		t.Fatalf("reused pending ID silently selected a different canonical execution: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryHistoryEventsByKeys(t.Context(), []string{canonical.EventKey}); err != nil || len(events) != 1 || events[0].Command != canonical.Command {
		t.Fatalf("failed confirmation removed the changed execution: %+v %v", events, err)
	}
}

func TestHistoryForgetDirectoryVisitsFollowExecutionIdentity(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	visits, ok := any(store).(historyDirectoryStore)
	if !ok {
		t.Fatal("directory visits cannot be attributed to their recorded execution")
	}
	at := time.Now().UTC().Add(-time.Hour)
	for index, id := range []string{"first-visit", "second-visit"} {
		completedForgetFixture(t, store, id, "z project", at.Add(time.Duration(index)*time.Minute))
		for range 2 {
			if err := visits.RecordHistoryDirectory(t.Context(), id, "/project"); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 2 {
		t.Fatalf("duplicate completion must not inflate directory visits: %+v %v", entries, err)
	}
	if removed, err := forgettingStore(t, store).ForgetHistoryEvents(t.Context(), []string{"first-visit"}); err != nil || removed != 1 {
		t.Fatalf("forget attributed visit: removed=%d error=%v", removed, err)
	}
	if err := visits.RecordHistoryDirectory(t.Context(), "first-visit", "/project"); !errors.Is(err, ErrHistoryForgotten) {
		t.Fatalf("late visit must be deliberately rejected: %v", err)
	}
	entries, err = store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("remaining visit must retain only its own evidence: %+v %v", entries, err)
	}
	if err := store.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := visits.RecordHistoryDirectory(t.Context(), "second-visit", "/project"); err == nil {
		t.Fatal("late visit restored a cleared execution")
	}
	entries, err = store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 0 {
		t.Fatalf("clear must remove execution-derived visits: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryPreservesLegacyDuplicatesAndExternalEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC().Add(-time.Hour)
	for index, id := range []string{"old-one", "old-two", "old-three"} {
		completedForgetFixture(t, store, id, "cd /legacy", at.Add(time.Duration(index)*time.Minute))
		if err := store.RecordDirectory(t.Context(), "/legacy"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceDirectorySource(t.Context(), "zoxide", []DirectoryImport{{Path: "/external", Count: 4}}); err != nil {
		t.Fatal(err)
	}
	// Equal cardinality does not supply the missing membership/provenance.
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{"old-one"}); err == nil || removed != 0 {
		t.Fatalf("unowned legacy visits require refusal: removed=%d error=%v", removed, err)
	}
	entries, err := store.QueryDirectories(t.Context(), "/legacy", 10, "frequent")
	if err != nil || len(entries) != 1 || entries[0].Count != 3 {
		t.Fatalf("refused deletion must preserve all unowned visits: %+v %v", entries, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 3 {
		t.Fatalf("refused deletion changed canonical executions: %+v %v", events, err)
	}
	entries, err = store.QueryDirectories(t.Context(), "/external", 10, "frequent")
	if err != nil || len(entries) != 1 || entries[0].Count != 4 {
		t.Fatalf("forget changed unrelated external evidence: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryDoesNotGuessUnattributedLegacyDestination(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	completedForgetFixture(t, store, "old-z", "z project", time.Now().UTC().Add(-time.Hour))
	for _, path := range []string{"/project", "/unrelated"} {
		if err := store.RecordDirectory(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{"old-z"}); err == nil || removed != 0 {
		t.Fatalf("unattributable deletion must fail without claiming success: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != "old-z" {
		t.Fatalf("unsafe deletion changed canonical history: %+v %v", events, err)
	}
	for _, path := range []string{"/project", "/unrelated"} {
		entries, err := store.QueryDirectories(t.Context(), path, 10, "frequent")
		if err != nil || len(entries) != 1 || entries[0].Count != 1 {
			t.Fatalf("unsafe deletion changed directory evidence: path=%s entries=%+v %v", path, entries, err)
		}
	}
}

func TestHistoryForgetDirectoryRefusesMixedUnattributedLegacyEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC().Add(-time.Hour)
	for index := range 5 {
		id, command := fmt.Sprintf("old-z-%d", index), "z project"
		if index == 4 {
			id, command = "old-cd", "cd /project"
		}
		completedForgetFixture(t, store, id, command, at.Add(time.Duration(index)*time.Minute))
		if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{"old-cd"}); err == nil || removed != 0 {
		t.Fatalf("mixed legacy deletion must refuse rather than erase unrelated visits: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 5 {
		t.Fatalf("refused deletion changed canonical executions: %+v %v", events, err)
	}
	if entries, err := store.QueryDirectories(t.Context(), "/project", 10, "frequent"); err != nil || len(entries) != 1 || entries[0].Count != 5 {
		t.Fatalf("refused deletion changed independent directory evidence: %+v %v", entries, err)
	}
	if forgotten, err := store.HistoryEventForgotten(t.Context(), "old-cd"); err != nil || forgotten {
		t.Fatalf("refused deletion installed an exclusion: %v %v", forgotten, err)
	}
	if changes, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256); err != nil || len(changes.Changes) != 0 {
		t.Fatalf("refused deletion published a reset: %+v %v", changes, err)
	}
}

func TestHistoryForgetDirectoryRefusesUnmatchedLegacyVisitCardinality(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	completedForgetFixture(t, store, "old-cd", "cd /project", time.Now().UTC().Add(-time.Hour))
	// Prior count pruning could remove old canonical executions without
	// reconciling their path-only visits. Those visits have no owner ID.
	for range 5 {
		if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{"old-cd"}); err == nil || removed != 0 {
		t.Fatalf("excess legacy visits require refusal: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != "old-cd" {
		t.Fatalf("refused deletion changed its execution: %+v %v", events, err)
	}
	if entries, err := store.QueryDirectories(t.Context(), "/project", 10, "frequent"); err != nil || len(entries) != 1 || entries[0].Count != 5 {
		t.Fatalf("refused deletion changed unowned visits: %+v %v", entries, err)
	}
	if forgotten, err := store.HistoryEventForgotten(t.Context(), "old-cd"); err != nil || forgotten {
		t.Fatalf("refused deletion installed an exclusion: %v %v", forgotten, err)
	}
	if changes, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256); err != nil || len(changes.Changes) != 0 {
		t.Fatalf("refused deletion published a reset: %+v %v", changes, err)
	}
}

func TestHistoryForgetDirectoryRejectsNonNavigationAndDestinationRewrite(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	completedForgetFixture(t, store, "not-navigation", "git status", time.Now().UTC())
	if err := store.RecordHistoryDirectory(t.Context(), "not-navigation", "/project"); err == nil {
		t.Fatal("ordinary activity became navigation evidence")
	}
	completedForgetFixture(t, store, "navigation", "z project", time.Now().UTC())
	if err := store.RecordHistoryDirectory(t.Context(), "navigation", "/project"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHistoryDirectory(t.Context(), "navigation", "/rewritten"); err == nil {
		t.Fatal("a delayed update rewrote the actual navigation destination")
	}
	if err := store.RefreshHistoryDirectories(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("refresh lost the actual z destination: %+v %v", entries, err)
	}
	entries, err = store.QueryDirectories(t.Context(), "/rewritten", 10, "balanced")
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected destination affected ranking: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryCountRetentionProtectsUnownedLegacyEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC().Add(-time.Hour)
	for index, path := range []string{"/pruned", "/retained"} {
		completedForgetFixture(t, store, []string{"old-visit", "new-visit"}[index], "cd "+path, at.Add(time.Duration(index)*time.Minute))
		if err := store.RecordDirectory(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := store.PruneHistoryEvents(t.Context(), 1); err == nil || removed != 0 {
		t.Fatalf("unowned retention requires refusal: removed=%d error=%v", removed, err)
	}
	entries, err := store.QueryDirectories(t.Context(), "/pruned", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("refused retention changed old directory evidence: %+v %v", entries, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatalf("refused retention changed canonical executions: %+v %v", events, err)
	}
	entries, err = store.QueryDirectories(t.Context(), "/retained", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("retention damaged surviving visit: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryRollingWindowHandlesExpiryAndLateVisits(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Hour)
	completedForgetFixture(t, store, "expires", "z project", now.Add(-DirectoryRecentWindow+time.Hour))
	if err := store.recordHistoryDirectoryAt(t.Context(), "expires", "/project", now); err != nil {
		t.Fatal(err)
	}
	completedForgetFixture(t, store, "fresh", "z project", now.Add(2*time.Hour))
	if err := store.recordHistoryDirectoryAt(t.Context(), "fresh", "/project", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 2 || entries[0].RecentCount != 1 {
		t.Fatalf("expired lifetime frequency must not remain recent: %+v %v", entries, err)
	}
	completedForgetFixture(t, store, "late", "z project", now.Add(-DirectoryRecentWindow+150*time.Minute))
	if err := store.recordHistoryDirectoryAt(t.Context(), "late", "/project", now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err = store.QueryDirectories(t.Context(), "/project", 10, "balanced")
	if err != nil || len(entries) != 1 || entries[0].Count != 3 || entries[0].RecentCount != 1 {
		t.Fatalf("late old visit must not decrement or inflate current-window evidence: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryCombinesLegacyAndNewVisitsImmediately(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC().Add(-time.Hour)
	for index := range 4 {
		completedForgetFixture(t, store, fmt.Sprintf("legacy-%d", index), "cd /project", at.Add(time.Duration(index)*time.Minute))
		if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RefreshHistoryDirectories(t.Context()); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		id := fmt.Sprintf("attributed-%d", index)
		completedForgetFixture(t, store, id, "z project", at.Add(time.Duration(index+4)*time.Minute))
		if err := store.RecordHistoryDirectory(t.Context(), id, "/project"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.QueryDirectories(t.Context(), "/project", 10, "frequent")
	if err != nil || len(entries) != 1 || entries[0].Count != 7 {
		t.Fatalf("immediate directory evidence must include four old and three new visits, without double counting: %+v %v", entries, err)
	}
}

func TestHistoryForgetDirectoryCombinesUnknownLegacyWithNewAttributedVisits(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC().Add(-time.Hour)
	for index := range 4 {
		completedForgetFixture(t, store, fmt.Sprintf("old-z-%d", index), "z project", at.Add(time.Duration(index)*time.Minute))
		if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RefreshHistoryDirectories(t.Context()); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		id := fmt.Sprintf("new-z-%d", index)
		completedForgetFixture(t, store, id, "z project", at.Add(time.Duration(index+4)*time.Minute))
		if err := store.RecordHistoryDirectory(t.Context(), id, "/project"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceDirectorySource(t.Context(), "zoxide", []DirectoryImport{{Path: "/project", Count: 1, LastUsed: at.Add(-24 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		entries, err := store.QueryDirectories(t.Context(), "/project", 10, "frequent")
		if err != nil || len(entries) != 1 || entries[0].Count != want {
			t.Fatalf("unknown legacy and distinct new activity: want=%d got=%+v error=%v", want, entries, err)
		}
	}
	assertCount(7)
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{"new-z-0"}); err != nil || removed != 1 {
		t.Fatalf("forget attributed survivor: removed=%d error=%v", removed, err)
	}
	assertCount(6)
	if err := store.RefreshHistoryDirectories(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertCount(6)
}

func forgettingStore(t *testing.T, store *FrecencyStore) historyForgetStore {
	t.Helper()
	forgetter, ok := any(store).(historyForgetStore)
	if !ok {
		t.Fatal("history store cannot forget selected executions")
	}
	return forgetter
}

func completedForgetFixture(t *testing.T, store *FrecencyStore, id, command string, at time.Time) HistoryEvent {
	t.Helper()
	event := HistoryEvent{EventKey: id, Command: command, NormalizedCommand: command, Cwd: "/repo", Source: "vuja", SessionID: "fixture", SubmittedAt: at, StartedAt: at}
	if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.CompletedAt, event.HasExitCode, event.State = at.Add(time.Second), true, "completed"
	if changed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !changed {
		t.Fatalf("fixture completion: changed=%v error=%v", changed, err)
	}
	return event
}

func TestHistoryForgetRemovesOnlySelectedExecutionAndRebuildsEvidence(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	completedForgetFixture(t, store, "first", "ssh forge@one", at)
	completedForgetFixture(t, store, "second", "ssh forge@one", at.Add(time.Minute))
	completedForgetFixture(t, store, "third", "git status", at.Add(2*time.Minute))
	if err := store.RecordExecutionFeedback(t.Context(), "first", "ssh forge@one", "/repo", "accepted"); err != nil {
		t.Fatal(err)
	}
	before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := forgettingStore(t, store).ForgetHistoryEvents(t.Context(), []string{"first", "first", "absent"})
	if err != nil || removed != 1 {
		t.Fatalf("forget one distinct execution: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 2 {
		t.Fatalf("expected exactly two surviving executions: %+v %v", events, err)
	}
	for _, event := range events {
		if event.EventKey != "second" && event.EventKey != "third" {
			t.Fatalf("unexpected survivor: %+v", event)
		}
	}
	entries, err := store.QueryLocal(t.Context(), "/repo", "ssh", 10)
	if err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("surviving duplicate must have count one: %+v %v", entries, err)
	}
	values, err := store.QueryArgumentValues(t.Context(), "/repo", "/repo", "ssh", 1, "forge@", 10)
	if err != nil || len(values) != 1 || values[0].Count != 1 {
		t.Fatalf("forgotten argument evidence survived: %+v %v", values, err)
	}
	feedback, err := store.QueryFeedback(t.Context(), "/repo", "/repo", "ssh", 10)
	if err != nil || len(feedback) != 0 {
		t.Fatalf("selected execution feedback must not retain forgotten evidence: %+v %v", feedback, err)
	}
	changes, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256)
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Kind != "reset" {
		t.Fatalf("other sessions need an atomic reset: %+v %v", changes, err)
	}
}

func TestHistoryForgetSurvivesReopenLateCompletionAndReimport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := completedForgetFixture(t, store, "forgotten", "ssh forge@one", at)
	if removed, err := forgettingStore(t, store).ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget: removed=%d error=%v", removed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordHistorySubmission(t.Context(), forgotten); err == nil {
		t.Fatal("a delayed submission recreated a forgotten identity")
	}
	if changed, err := store.CompleteHistoryEvent(t.Context(), forgotten); err != nil || changed {
		t.Fatalf("forgotten completion must be a successful discarded no-op: changed=%v error=%v", changed, err)
	}
	forgotten.Imported = true
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{forgotten}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("reimport resurrected a forgotten identity: %+v %v", events, err)
	}
	completedForgetFixture(t, store, "genuine-future", "ssh forge@one", at.Add(time.Hour))
	if entries, err := store.QueryLocal(t.Context(), "/repo", "ssh", 10); err != nil || len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("new genuine execution must remain eligible: %+v %v", entries, err)
	}
}

func TestHistoryForgetNativeExternalCopiesPreserveNearbyIndependentExecutions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	forgotten := completedForgetFixture(t, store, "native-forgotten", "ssh forge@one", at)
	kept := completedForgetFixture(t, store, "native-kept", "ssh forge@one", at.Add(300*time.Millisecond))
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	copy := forgotten
	copy.EventKey, copy.Source, copy.Imported = "atuin:copy", "atuin", true
	copy.SubmittedAt, copy.CompletedAt = time.Time{}, time.Time{}
	keptCopy := copy
	keptCopy.EventKey, keptCopy.StartedAt = "atuin:kept", kept.StartedAt
	extra := copy
	extra.EventKey, extra.StartedAt = "atuin:extra", at.Add(900*time.Millisecond)
	otherDirectory := copy
	otherDirectory.EventKey, otherDirectory.Cwd = "atuin:other-directory", "/other"
	otherOutcome := copy
	otherOutcome.EventKey, otherOutcome.ExitCode, otherOutcome.State = "atuin:other-outcome", 17, "failed"
	unknownTime := copy
	unknownTime.EventKey, unknownTime.StartedAt = "bash:unknown", time.Time{}
	unknownTime.Source = "bash"
	shellCopy := copy
	shellCopy.EventKey, shellCopy.Source, shellCopy.Cwd = "zsh:shifted-copy", "zsh", ""
	shellCopy.HasExitCode = false
	// Input order deliberately places the genuine nearby repeat before the
	// forgotten counterpart. Matching cannot just discard the first prefix.
	imports := []HistoryEvent{keptCopy, extra, otherDirectory, otherOutcome, unknownTime, copy, shellCopy}
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.ReplaceImportedHistoryEvents(t.Context(), imports); err != nil {
			t.Fatal(err)
		}
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ids := make(map[string]bool)
		for _, event := range events {
			ids[event.EventKey] = true
		}
		for _, id := range []string{copy.EventKey, shellCopy.EventKey} {
			if ids[id] {
				t.Errorf("external counterpart resurrected after reopen/refresh: %s", id)
			}
		}
		for _, id := range []string{kept.EventKey, keptCopy.EventKey, extra.EventKey, otherDirectory.EventKey, otherOutcome.EventKey, unknownTime.EventKey} {
			if !ids[id] {
				t.Errorf("forgetting removed independent execution: %s", id)
			}
		}
		if len(ids) != 6 {
			t.Errorf("expected six independent surviving identities, got %+v", ids)
		}
	}
}

func TestHistoryForgetInvalidImportsCannotConsumeAnExclusionSlot(t *testing.T) {
	for _, kind := range []string{"missing ID", "empty command", "leading space", "terminal control", "invalid state"} {
		t.Run(kind, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			forgotten := completedForgetFixture(t, store, "native-forgotten", "ssh forge@one", at)
			if _, err := store.ForgetHistoryEvents(t.Context(), []string{forgotten.EventKey}); err != nil {
				t.Fatal(err)
			}
			invalid := forgotten
			invalid.EventKey, invalid.Source, invalid.Imported = "atuin:invalid", "atuin", true
			switch kind {
			case "missing ID":
				invalid.EventKey = ""
			case "empty command":
				invalid.Command = ""
			case "leading space":
				invalid.Command = " " + invalid.Command
			case "terminal control":
				invalid.Command += "\x1b[?1000h"
			case "invalid state":
				invalid.State = "not-a-history-state"
			}
			validCopy := forgotten
			validCopy.EventKey, validCopy.Source, validCopy.Imported = "atuin:real-copy", "atuin", true
			validCopy.StartedAt = at.Add(100 * time.Millisecond)
			if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{invalid, validCopy}); err != nil {
				t.Fatal(err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
				t.Fatalf("invalid external row consumed the forgotten slot and restored its valid counterpart: %+v %v", events, err)
			}
		})
	}
}

func TestHistoryForgetAlreadyExcludedImportsCannotConsumeACounterpartSlot(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	native := completedForgetFixture(t, store, "native-forgotten", "ssh forge@one", at)
	previous := native
	previous.EventKey, previous.Source, previous.Imported = "atuin:already-excluded", "atuin", true
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{previous}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{previous.EventKey, native.EventKey}); err != nil {
		t.Fatal(err)
	}
	counterpart := previous
	counterpart.EventKey, counterpart.StartedAt = "atuin:native-counterpart", at.Add(100*time.Millisecond)
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{previous, counterpart}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("an already-excluded row displaced the native counterpart exclusion: %+v %v", events, err)
	}
}

func TestHistoryForgetRepeatedImportedIdentityCannotRestoreCounterpart(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := completedForgetFixture(t, store, "native-forgotten", "ssh forge@one", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{native.EventKey}); err != nil {
		t.Fatal(err)
	}
	copy := native
	copy.EventKey, copy.Source, copy.Imported = "atuin:duplicate-input", "atuin", true
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{copy, copy}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("repeating one imported identity bypassed its counterpart exclusion: %+v %v", events, err)
	}
}

func TestHistoryForgetNoMatchCancellationAndRollbackChangeNothing(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	completedForgetFixture(t, store, "keep", "git status", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	forgetter := forgettingStore(t, store)
	before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := forgetter.ForgetHistoryEvents(t.Context(), []string{"absent"}); err != nil || removed != 0 {
		t.Fatalf("no-match: removed=%d error=%v", removed, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if removed, err := forgetter.ForgetHistoryEvents(cancelled, []string{"keep"}); removed != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: removed=%d error=%v", removed, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_forget BEFORE DELETE ON history_entries BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if removed, err := forgetter.ForgetHistoryEvents(t.Context(), []string{"keep"}); removed != 0 || err == nil {
		t.Fatalf("failed projection rebuild must roll back forgetting: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != "keep" {
		t.Fatalf("failed forgetting changed canonical data: %+v %v", events, err)
	}
	after, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256)
	if err != nil || len(after.Changes) != 0 {
		t.Fatalf("no-op/failed forgetting published a change: %+v %v", after, err)
	}
}

func TestHistoryForgetReimportDoesNotRestoreAnExecutionUnderANewShellRowID(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := HistoryEvent{EventKey: "zsh:old-position", Command: "ssh forge@one", NormalizedCommand: "ssh forge@one", Cwd: "/repo", StartedAt: at, Source: "zsh", State: "unknown", Imported: true}
	second := first
	second.EventKey, second.StartedAt = "zsh:keep", at.Add(time.Hour)
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{first, second}); err != nil {
		t.Fatal(err)
	}
	if removed, err := forgettingStore(t, store).ForgetHistoryEvents(t.Context(), []string{first.EventKey}); err != nil || removed != 1 {
		t.Fatalf("forget: removed=%d error=%v", removed, err)
	}
	// Shell adapters include row position in synthetic IDs. Removing an older
	// unrelated line must not let an already-forgotten execution return.
	first.EventKey = "zsh:new-position"
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{first, second}); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != second.EventKey {
		t.Fatalf("reimport restored forgotten execution metadata under a changed ID: %+v %v", events, err)
	}
}

func TestHistoryForgetAmbiguousImportedExecutionsRequireTheWholeGroup(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := HistoryEvent{EventKey: "bash:one", Command: "ssh forge@one", NormalizedCommand: "ssh forge@one", Source: "bash", State: "unknown", Imported: true}
	second := first
	second.EventKey = "bash:two"
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{first, second}); err != nil {
		t.Fatal(err)
	}
	if removed, err := forgettingStore(t, store).ForgetHistoryEvents(t.Context(), []string{first.EventKey}); removed != 0 || err == nil {
		t.Fatalf("missing execution metadata cannot justify silently forgetting only an ambiguous copy: removed=%d error=%v", removed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatalf("rejected ambiguous selection changed history: %+v %v", events, err)
	}
	if removed, err := store.ForgetHistoryEvents(t.Context(), []string{first.EventKey, second.EventKey}); removed != 2 || err != nil {
		t.Fatalf("explicit selection of the full ambiguous group should be supported: removed=%d error=%v", removed, err)
	}
	first.EventKey, second.EventKey = "bash:changed-one", "bash:changed-two"
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{first, second}); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("unknown-timestamp imported copies resurrected a forgotten group: %+v %v", events, err)
	}
}

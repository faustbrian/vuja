package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/pflag"
)

func historyArchiveFixture(t *testing.T) *scoring.FrecencyStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	previous := config.Get().History
	config.Get().History = config.DefaultConfig().History
	t.Cleanup(func() { config.Get().History = previous })
	recall := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(recall) })
	store, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// QA3: the public JSON surface must have the same stable date domain as the
// canonical store, including explicit unknown and UTC-normalized boundaries.
func TestHistoryArchiveCodecTimestampDomain(t *testing.T) {
	for _, field := range []string{"submitted", "started", "completed"} {
		for _, fixture := range []struct {
			name  string
			stamp time.Time
			valid bool
		}{
			{"unknown", time.Time{}, true},
			{"epoch", time.Unix(0, 0).UTC(), false},
			{"pre-epoch", time.Unix(-1, 0).UTC(), false},
			{"year-zero", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), false},
			{"post-epoch-nanosecond", time.Unix(0, 1).UTC(), true},
			{"year-9999", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true},
			{"offset", time.Date(2026, 10, 1, 15, 0, 0, 123, time.FixedZone("fixture", 3*3600)), true},
			{"UTC-overflow", time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("fixture", -14*3600)), false},
		} {
			t.Run(field+"/"+fixture.name, func(t *testing.T) {
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				imported, aggregate := false, false
				record := historyArchiveRecord{ID: "vuja:codec", Command: "pwd", NormalizedCommand: "pwd", Cwd: "/repo", SubmittedAt: at, StartedAt: at, CompletedAt: at, Source: "vuja", State: "unknown", HistoryOrder: 1, Occurrences: 1, Imported: &imported, Aggregate: &aggregate}
				switch field {
				case "submitted":
					record.SubmittedAt = fixture.stamp
				case "started":
					record.StartedAt = fixture.stamp
				case "completed":
					record.CompletedAt = fixture.stamp
				}
				payload, err := json.Marshal(historyArchiveDocument{Format: historyArchiveFormat, Version: historyArchiveVersion, Events: []historyArchiveRecord{record}})
				if err != nil {
					t.Fatal(err)
				}
				events, err := decodeHistoryArchive(bytes.NewReader(payload))
				if !fixture.valid {
					if err == nil {
						t.Fatal("JSON admitted a date the store cannot preserve")
					}
					return
				}
				if err != nil || len(events) != 1 {
					t.Fatalf("representable JSON date rejected: %+v %v", events, err)
				}
				wantSubmitted := record.SubmittedAt.UTC()
				if wantSubmitted.IsZero() {
					wantSubmitted = record.StartedAt.UTC()
				}
				if !events[0].SubmittedAt.Equal(wantSubmitted) || !events[0].StartedAt.Equal(record.StartedAt) || !events[0].CompletedAt.Equal(record.CompletedAt) {
					t.Fatalf("known/unknown or offset semantics changed: %+v", events[0])
				}
				encoded, err := encodeHistoryArchive(events)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := decodeHistoryArchive(bytes.NewReader(encoded))
				if err != nil || len(restored) != 1 || restored[0] != events[0] {
					t.Fatalf("codec retry changed portable record: %+v %v", restored, err)
				}
			})
		}
	}
}

// Invoke only the finite archive handler, never root.Execute, a shell, a PTY,
// persistent root hooks, or a Vuja executable.
func runHistoryArchiveFixture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command, remaining, err := historyCmd.Find(args)
	if err != nil {
		return "", err
	}
	if command == historyCmd || command.RunE == nil {
		return "", errors.New("native history archive command is unavailable")
	}
	oldOut, oldContext := command.OutOrStdout(), command.Context()
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		value, changed := flag.Value.String(), flag.Changed
		t.Cleanup(func() { _ = flag.Value.Set(value); flag.Changed = changed })
	})
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetContext(context.Background())
	t.Cleanup(func() { command.SetOut(oldOut); command.SetContext(oldContext) })
	if err := command.ParseFlags(remaining); err != nil {
		return out.String(), err
	}
	parsed := command.Flags().Args()
	if err := command.ValidateArgs(parsed); err != nil {
		return out.String(), err
	}
	err = command.RunE(command, parsed)
	return out.String(), err
}

func TestHistoryArchiveCommandsRoundTripNativeExternalAndUnfinished(t *testing.T) {
	store := historyArchiveFixture(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 123, time.UTC)
	native := scoring.HistoryEvent{EventKey: "vuja:unfinished", Command: "\n\nprintf 'café 日本語'  ", NormalizedCommand: "printf 'café 日本語'", Cwd: "/repo",
		SubmittedAt: at, StartedAt: at, Source: "vuja", Host: "host-one", SessionID: "archive-fixture", Shell: "zsh", Occurrences: 1}
	if err := store.RecordHistorySubmission(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	external := scoring.HistoryEvent{EventKey: "atuin:finished", Command: "ssh forge@archive", Cwd: "/repo", StartedAt: at.Add(time.Hour),
		Source: "atuin", Host: "host-two", SessionID: "external-fixture", Shell: "bash", State: "completed", HasExitCode: true, Imported: true, Occurrences: 1}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []scoring.HistoryEvent{external}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	if _, err := runHistoryArchiveFixture(t, "export", path); err != nil {
		t.Fatalf("export canonical executions: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive is not owner-only: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Format  string           `json:"format"`
		Version int              `json:"version"`
		Events  []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.Format != "vuja-history" || document.Version != 1 || len(document.Events) != 2 {
		t.Fatalf("export is not a complete versioned native archive: %v", err)
	}
	scoring.CloseGlobalFrecencyStore()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "restored"))
	for index := range 2 {
		out, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted")
		if err != nil || !strings.Contains(out, "processes are not resumed") {
			t.Fatalf("native import %d: output=%q error=%v", index, out, err)
		}
	}
	restored, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	events, err := restored.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 2 {
		t.Fatalf("archive import lost or duplicated executions: %+v %v", events, err)
	}
	for _, event := range events {
		switch event.EventKey {
		case native.EventKey:
			if event.Command != native.Command || event.NormalizedCommand != native.NormalizedCommand || event.State != "interrupted" || event.HasExitCode || event.Imported || event.Host != native.Host || !event.StartedAt.Equal(at) {
				t.Fatalf("unfinished archive fabricated a completion or changed provenance: %+v", event)
			}
		case external.EventKey:
			if !event.Imported || event.Source != "atuin" || event.Host != external.Host || event.SessionID != external.SessionID {
				t.Fatalf("external provenance did not survive native portability: %+v", event)
			}
		default:
			t.Fatalf("unexpected restored execution %q", event.EventKey)
		}
	}
	if err := restored.ReplaceAdapterHistoryEvents(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if events, err := restored.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
		t.Fatal("optional source refresh erased a restored execution")
	}
	if recall := integration.RichHistorySnapshot(); len(recall) != 2 {
		t.Fatalf("native import did not publish canonical recall: %+v", recall)
	}
}

func TestHistoryArchiveExportsAtuinUnknownMetadataWithoutInventingOutcomes(t *testing.T) {
	store := historyArchiveFixture(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// Atuin uses -1 for imported unknown exit/duration. Older Vuja adapters
	// retained those values as a known failure and a negative duration.
	unknown := scoring.HistoryEvent{EventKey: "atuin:unknown", Command: "git status", Cwd: "/repo",
		StartedAt: at, Duration: -1, ExitCode: -1, HasExitCode: true, State: "failed",
		Source: "atuin", Host: "archive-host", SessionID: "import-session", Imported: true, Occurrences: 1}
	success := unknown
	success.EventKey, success.Command, success.ExitCode, success.State = "atuin:success", "pwd", 0, "completed"
	failure := unknown
	failure.EventKey, failure.Command, failure.ExitCode = "atuin:failure", "grep missing fixture", 7
	failure.Duration, failure.CompletedAt = 2*time.Second, at.Add(2*time.Second)
	if err := store.ReplaceAdapterHistoryEvents(t.Context(), []scoring.HistoryEvent{unknown, success, failure}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	if _, err := runHistoryArchiveFixture(t, "export", path); err != nil {
		t.Fatalf("export legitimate legacy Atuin unknown metadata: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := decodeHistoryArchive(bytes.NewReader(data))
	if err != nil || len(events) != 3 {
		t.Fatalf("decode Atuin archive: count=%d error=%v", len(events), err)
	}
	for _, event := range events {
		wantDuration, wantCompletion := time.Duration(0), time.Time{}
		if event.EventKey == failure.EventKey {
			wantDuration, wantCompletion = failure.Duration, failure.CompletedAt
		}
		if event.Duration != wantDuration || !event.CompletedAt.Equal(wantCompletion) || event.Source != "atuin" || !event.Imported ||
			event.Host != unknown.Host || event.SessionID != unknown.SessionID || !event.StartedAt.Equal(at) {
			t.Fatalf("unknown timing or original provenance was invented/changed: %+v", event)
		}
		switch event.EventKey {
		case unknown.EventKey:
			if event.State != "unknown" || event.HasExitCode {
				t.Fatalf("unknown imported outcome became a failure/success: %+v", event)
			}
		case success.EventKey:
			if event.State != "completed" || !event.HasExitCode || event.ExitCode != 0 {
				t.Fatalf("unknown duration erased an observed outcome: %+v", event)
			}
		case failure.EventKey:
			if event.State != "failed" || !event.HasExitCode || event.ExitCode != failure.ExitCode {
				t.Fatalf("known imported failure was erased: %+v", event)
			}
		default:
			t.Fatalf("portable identity changed: %q", event.EventKey)
		}
	}
	// Equal portable payloads must compare safely with the older stored shape.
	if _, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted"); err != nil {
		t.Fatalf("same-database Atuin round trip: %v", err)
	}
	scoring.CloseGlobalFrecencyStore()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "restored"))
	for range 2 {
		if _, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted"); err != nil {
			t.Fatalf("clean Atuin restore/idempotent retry: %v", err)
		}
	}
	restored, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.ReplaceAdapterHistoryEvents(t.Context(), []scoring.HistoryEvent{unknown, success, failure}); err != nil {
		t.Fatal(err)
	}
	got, err := restored.QueryAllHistoryEvents(t.Context())
	if err != nil || len(got) != 3 {
		t.Fatalf("adapter refresh lost or duplicated restored identities: %+v %v", got, err)
	}
	for _, event := range got {
		if event.Duration < 0 || event.EventKey == unknown.EventKey && (event.HasExitCode || event.State != "unknown") {
			t.Fatalf("adapter refresh replaced portable unknown semantics: %+v", event)
		}
	}
}

func TestHistoryArchiveIntegerOrderRoundTripIsExact(t *testing.T) {
	for _, order := range []int{1 << 53, math.MaxInt64} {
		event := scoring.HistoryEvent{EventKey: "vuja:exact", Command: "pwd", Source: "vuja", State: "unknown", HistoryOrder: order, Occurrences: 1}
		data, err := encodeHistoryArchive([]scoring.HistoryEvent{event})
		if err != nil {
			t.Fatalf("encode supported native order %d: %v", order, err)
		}
		decoded, err := decodeHistoryArchive(bytes.NewReader(data))
		if err != nil || len(decoded) != 1 || decoded[0].HistoryOrder != order {
			t.Fatalf("JSON integer round trip changed native order %d: %+v %v", order, decoded, err)
		}
		again, err := encodeHistoryArchive(decoded)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("re-export changed an exact integer order: %v", err)
		}
	}
}

func TestHistoryArchiveRetryPreservesPartiallyRetainedAggregate(t *testing.T) {
	store := historyArchiveFixture(t)
	config.Get().History.Retention, config.Get().History.MaxEvents = "bounded", 2
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	event := scoring.HistoryEvent{EventKey: "legacy:compact", Command: "git status", Cwd: "/repo",
		StartedAt: at, Source: "legacy-vuja", SessionID: "legacy-migration", State: "completed",
		HasExitCode: true, HistoryOrder: 1, Occurrences: 7}
	data, err := encodeHistoryArchive([]scoring.HistoryEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for step := range 3 {
		if step == 2 {
			scoring.CloseGlobalFrecencyStore()
		}
		if _, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted"); err != nil {
			t.Fatalf("retry archive after partial retention (step=%d): %v", step, err)
		}
		store, err = scoring.GetFrecencyStore()
		if err != nil {
			t.Fatal(err)
		}
		events, err := store.QueryAllHistoryEvents(t.Context())
		if err != nil || len(events) != 1 || events[0].EventKey != event.EventKey || events[0].Occurrences != 2 {
			t.Fatalf("archive retry reinflated or lost retained scope: %+v %v", events, err)
		}
		frequencies, err := store.QueryLocal(t.Context(), "/repo", event.Command, 10)
		if err != nil || len(frequencies) != 1 || frequencies[0].Count != 2 {
			t.Fatalf("archive retry changed retained frequency: %+v %v", frequencies, err)
		}
	}
	for _, kind := range []string{"changed count", "changed command"} {
		conflict := event
		if kind == "changed count" {
			conflict.Occurrences = 6
		} else {
			conflict.Command = "git log"
		}
		data, err := encodeHistoryArchive([]scoring.HistoryEvent{conflict})
		if err != nil {
			t.Fatal(err)
		}
		conflictPath := filepath.Join(t.TempDir(), "conflict.json")
		if err := os.WriteFile(conflictPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runHistoryArchiveFixture(t, "import", "native", conflictPath, "--old-sessions-restarted"); err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("retention retry exception weakened %s rejection: %v", kind, err)
		}
	}
	exported := filepath.Join(t.TempDir(), "retained.json")
	if _, err := runHistoryArchiveFixture(t, "export", exported); err != nil {
		t.Fatal(err)
	}
	retained, err := readHistoryArchiveFile(exported)
	if err != nil || len(retained) != 1 || retained[0].Occurrences != 2 || retained[0].EventKey != event.EventKey {
		t.Fatalf("export exposed original rather than retained scope: %+v %v", retained, err)
	}
}

// Goal QA 3/8: retention changes retained weight, never the original identity
// scope. Exercise both the current database and a portable clean restore.
func TestHistoryArchiveRetainedSingleOccurrenceDoesNotClaimAdapterExecution(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, source := range []string{"atuin", "zsh"} {
			t.Run(source+map[bool]string{false: "/retained", true: "/restored"}[restore], func(t *testing.T) {
				store := historyArchiveFixture(t)
				at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				aggregate := scoring.HistoryEvent{EventKey: "archive:compact", Command: "git status", NormalizedCommand: "git status", Cwd: "/repo",
					SubmittedAt: at, StartedAt: at, CompletedAt: at.Add(time.Second), Duration: time.Second,
					Source: "vuja", SessionID: "prior-session", State: "completed", HasExitCode: true, HistoryOrder: 1, Occurrences: 7}
				if _, err := store.ImportHistoryArchiveEvents(t.Context(), []scoring.HistoryEvent{aggregate}); err != nil {
					t.Fatal(err)
				}
				if removed, err := store.PruneHistoryEvents(t.Context(), 1); err != nil || removed != 6 {
					t.Fatalf("retain one occurrence: removed=%d error=%v", removed, err)
				}
				retained, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(retained) != 1 || retained[0].Occurrences != 1 {
					t.Fatalf("retained scope: %+v %v", retained, err)
				}
				changedScope := retained[0]
				changedScope.Aggregate = false
				if _, err := store.ImportHistoryArchiveEvents(t.Context(), []scoring.HistoryEvent{changedScope}); err == nil || !strings.Contains(err.Error(), "conflict") {
					t.Fatalf("same-ID aggregate scope was silently downgraded: %v", err)
				}
				cleanPath := ""
				if restore {
					data, err := encodeHistoryArchive(retained)
					if err != nil {
						t.Fatal(err)
					}
					portable, err := decodeHistoryArchive(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					cleanPath = filepath.Join(t.TempDir(), "clean.db")
					clean, err := scoring.NewFrecencyStore(cleanPath)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = clean.Close() })
					if _, err := clean.ImportHistoryArchiveEvents(t.Context(), portable); err != nil {
						t.Fatal(err)
					}
					store = clean
				} else if added, err := store.ImportHistoryArchiveEvents(t.Context(), []scoring.HistoryEvent{aggregate}); err != nil || added != 0 {
					t.Fatalf("original archive retry: added=%d error=%v", added, err)
				}
				adapter := aggregate
				adapter.EventKey, adapter.Source, adapter.Imported, adapter.Occurrences = "adapter:independent", source, true, 1
				adapter.StartedAt = at.Add(100 * time.Millisecond)
				for step := range 2 {
					if step == 1 {
						if restore {
							if err := store.Close(); err != nil {
								t.Fatal(err)
							}
							store, err = scoring.NewFrecencyStore(cleanPath)
							t.Cleanup(func() { _ = store.Close() })
						} else {
							scoring.CloseGlobalFrecencyStore()
							store, err = scoring.GetFrecencyStore()
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := store.ReplaceAdapterHistoryEvents(t.Context(), []scoring.HistoryEvent{adapter}); err != nil {
						t.Fatal(err)
					}
					events, err := store.QueryAllHistoryEvents(t.Context())
					if err != nil || len(events) != 2 {
						t.Fatalf("retained aggregate claimed an independent adapter execution: %+v %v", events, err)
					}
					for _, event := range events {
						if event.EventKey == aggregate.EventKey && (!event.Aggregate || event.Occurrences != 1) {
							t.Fatalf("aggregate scope or retained count was lost: %+v", event)
						}
					}
					frequencies, err := store.QueryLocal(t.Context(), "/repo", aggregate.Command, 10)
					if err != nil || len(frequencies) != 1 || frequencies[0].Count != 2 {
						t.Fatalf("independent retained evidence changed: %+v %v", frequencies, err)
					}
				}
				if removed, err := store.ForgetHistoryEvents(t.Context(), []string{aggregate.EventKey}); err != nil || removed != 1 {
					t.Fatalf("forget only the retained aggregate: removed=%d error=%v", removed, err)
				}
				if err := store.ReplaceAdapterHistoryEvents(t.Context(), []scoring.HistoryEvent{adapter}); err != nil {
					t.Fatal(err)
				}
				events, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(events) != 1 || events[0].EventKey != adapter.EventKey {
					t.Fatalf("forgetting the aggregate consumed an independent execution: %+v %v", events, err)
				}
				// The original archive includes occurrences beyond the retained
				// forgotten subset. An explicit compact-scope refusal is safe;
				// neither it nor a no-op may resurrect or consume another ID.
				if added, err := store.ImportHistoryArchiveEvents(t.Context(), []scoring.HistoryEvent{aggregate}); added != 0 || err != nil && !errors.Is(err, scoring.ErrHistoryCompactCounterpart) {
					t.Fatalf("forgotten archive changed history or failed unexpectedly: added=%d error=%v", added, err)
				}
				if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != adapter.EventKey {
					t.Fatalf("forgotten archive retry changed independent history: %+v %v", events, err)
				}
			})
		}
	}
}

func TestHistoryArchiveRequiresExplicitIdentityScope(t *testing.T) {
	for _, field := range []string{"aggregate", "imported"} {
		for _, missing := range []bool{true, false} {
			t.Run(field+map[bool]string{true: "/missing", false: "/null"}[missing], func(t *testing.T) {
				event := scoring.HistoryEvent{EventKey: "archive:one", Command: "git status", Source: "vuja",
					State: "completed", HasExitCode: true, HistoryOrder: 1, Occurrences: 1}
				data, err := encodeHistoryArchive([]scoring.HistoryEvent{event})
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					Format  string                       `json:"format"`
					Version int                          `json:"version"`
					Events  []map[string]json.RawMessage `json:"events"`
				}
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatal(err)
				}
				if missing {
					delete(document.Events[0], field)
				} else {
					document.Events[0][field] = json.RawMessage("null")
				}
				data, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := decodeHistoryArchive(bytes.NewReader(data)); err == nil {
					t.Fatalf("archive inferred missing/null %s metadata", field)
				}
			})
		}
	}
}

func TestHistoryArchiveExportsCompatibilityWritesBeforeAndAfterReopen(t *testing.T) {
	store := historyArchiveFixture(t)
	if err := store.Record(t.Context(), "git status", "/repo", 0); err != nil {
		t.Fatal(err)
	}
	for _, reopen := range []bool{false, true} {
		if reopen {
			scoring.CloseGlobalFrecencyStore()
		}
		path := filepath.Join(t.TempDir(), "archive.json")
		if _, err := runHistoryArchiveFixture(t, "export", path); err != nil {
			t.Fatalf("export supported compatibility write (reopen=%t): %v", reopen, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		events, err := decodeHistoryArchive(bytes.NewReader(data))
		if err != nil || len(events) != 1 || events[0].Command != "git status" || events[0].Imported {
			t.Fatalf("export lost compatibility execution: %+v %v", events, err)
		}
	}
}

func TestHistoryArchivePreparationCannotEraseUnattributedLearning(t *testing.T) {
	for _, operation := range []string{"import", "export"} {
		for _, retention := range []string{"count", "age"} {
			t.Run(operation+"/"+retention, func(t *testing.T) {
				store := historyArchiveFixture(t)
				now := time.Now().UTC()
				for index, at := range []time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)} {
					event := scoring.HistoryEvent{EventKey: fmt.Sprint("native:", index), Command: "pwd", Cwd: "/repo",
						Source: "vuja", SessionID: "native-session", SubmittedAt: at, StartedAt: at,
						CompletedAt: at.Add(time.Second), Duration: time.Second, State: "completed", HasExitCode: true}
					if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
						t.Fatal(err)
					}
					if changed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !changed {
						t.Fatalf("seed canonical execution: changed=%t error=%v", changed, err)
					}
				}
				if err := store.RecordTransition(t.Context(), "git checkout", "npm run dev", "/repo", 0); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordExactTransition(t.Context(), "git checkout feature", "npm run dev", "/repo", 0); err != nil {
					t.Fatal(err)
				}
				exactBefore, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
				skeletonBefore, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
				if len(exactBefore) != 1 || len(skeletonBefore) != 1 {
					t.Fatal("compatibility learning was not seeded")
				}
				changesBefore, err := store.QueryHistoryChanges(t.Context(), 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				config.Get().History.Retention, config.Get().History.MaxEvents = "bounded", 1
				if retention == "age" {
					config.Get().History.Retention = "unlimited"
					config.Get().History.MaxAge = config.Duration(24 * time.Hour)
				}
				path := filepath.Join(t.TempDir(), "archive.json")
				args := []string{"export", path}
				if operation == "import" {
					event := scoring.HistoryEvent{EventKey: "archive:new", Command: "git log", Cwd: "/repo",
						Source: "atuin", Imported: true, StartedAt: now, State: "completed", HasExitCode: true, Occurrences: 1}
					data, err := encodeHistoryArchive([]scoring.HistoryEvent{event})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					args = []string{"import", "native", path, "--old-sessions-restarted"}
				}
				_, commandErr := runHistoryArchiveFixture(t, args...)
				exactAfter, _ := store.QueryExactTransitionsWithFallback(t.Context(), "git checkout feature", "/repo")
				skeletonAfter, _ := store.QueryTransitionsWithFallback(t.Context(), "git checkout", "/repo")
				if len(exactAfter) != 1 || exactAfter[0] != exactBefore[0] || len(skeletonAfter) != 1 || skeletonAfter[0] != skeletonBefore[0] {
					t.Fatalf("archive preparation erased compatibility learning: exact=%+v skeleton=%+v command-error=%v", exactAfter, skeletonAfter, commandErr)
				}
				if operation == "import" && (commandErr == nil || !strings.Contains(commandErr.Error(), "unattributed learning")) {
					t.Fatalf("unsafe native merge was not refused: %v", commandErr)
				}
				if operation == "export" && commandErr != nil {
					t.Fatalf("deferred retention prevented canonical backup: %v", commandErr)
				}
				rows, err := store.QueryAllHistoryEvents(t.Context())
				if err != nil || len(rows) != 2 {
					t.Fatalf("preparation committed retention or archive rows despite unsafe reconstruction: %+v %v", rows, err)
				}
				changesAfter, err := store.QueryHistoryChanges(t.Context(), 0, 100)
				if err != nil || changesAfter.Latest != changesBefore.Latest {
					t.Fatalf("unsafe preparation published a new generation: %+v %v", changesAfter, err)
				}
			})
		}
	}
}

func TestHistoryArchiveInvalidInputIsRejectedBeforeOpeningTheStore(t *testing.T) {
	for _, fixture := range []struct{ name, document, reason string }{
		{"future version", "{\"format\":\"vuja-history\",\"version\":999,\"events\":[]}", "version"},
		{"wrong format", "{\"format\":\"other\",\"version\":1,\"events\":[]}", "format"},
		{"truncated", "{\"format\":\"vuja-history\",\"version\":1,\"events\":[", "archive"},
		{"unknown fields", "{\"format\":\"vuja-history\",\"version\":1,\"events\":[],\"extra\":true}", "archive"},
		{"trailing document", "{\"format\":\"vuja-history\",\"version\":1,\"events\":[]} {}", "archive"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dataHome := filepath.Join(t.TempDir(), "data")
			t.Setenv("XDG_DATA_HOME", dataHome)
			scoring.CloseGlobalFrecencyStore()
			t.Cleanup(scoring.CloseGlobalFrecencyStore)
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, []byte(fixture.document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), fixture.reason) {
				t.Fatalf("expected a %s rejection, got %v", fixture.reason, err)
			}
			assertHistoryArchiveStoreUntouched(t, dataHome)
		})
	}
}

func TestHistoryArchiveExportDoesNotOverwriteExistingFiles(t *testing.T) {
	historyArchiveFixture(t)
	path := filepath.Join(t.TempDir(), "archive.json")
	original := []byte("existing backup")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runHistoryArchiveFixture(t, "export", path)
	if err == nil || !strings.Contains(err.Error(), "exist") {
		t.Fatalf("expected existing-file refusal, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("export overwrote an existing backup")
	}
}

func TestHistoryArchiveImportRequiresRestartAcknowledgement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("{\"format\":\"vuja-history\",\"version\":1,\"events\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runHistoryArchiveFixture(t, "import", "native", path)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("expected legacy-writer restart guidance, got %v", err)
	}
	assertHistoryArchiveStoreUntouched(t, dataHome)
}

func TestHistoryArchiveImportRejectsUnsafeFilesAndSizeBeforeOpeningStore(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dataHome := filepath.Join(t.TempDir(), "data")
			t.Setenv("XDG_DATA_HOME", dataHome)
			scoring.CloseGlobalFrecencyStore()
			t.Cleanup(scoring.CloseGlobalFrecencyStore)
			dir := t.TempDir()
			path, reason := filepath.Join(dir, "input"), "regular"
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(scoring.MaxHistoryArchiveBytes + 1)
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("oversized fixture: %v %v", err, closeErr)
				}
				reason = "size"
			}
			_, err := runHistoryArchiveFixture(t, "import", "native", path, "--old-sessions-restarted")
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("expected %s boundary, got %v", reason, err)
			}
			assertHistoryArchiveStoreUntouched(t, dataHome)
		})
	}
}

func assertHistoryArchiveStoreUntouched(t *testing.T, dataHome string) {
	t.Helper()
	path, err := scoring.DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, dataHome} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid archive opened or changed history storage")
		}
	}
	if scoring.LoadedFrecencyStore() != nil {
		t.Fatal("invalid archive initialized the canonical store")
	}
}

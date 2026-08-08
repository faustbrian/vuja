package root

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/cachemetrics"

	"github.com/faustbrian/vuja/internal/config"
)

const latencySampleLimit = 256

type latencySnapshot struct {
	UpdatedAt     time.Time                    `json:"updated_at"`
	SessionID     string                       `json:"session_id"`
	PID           int                          `json:"pid"`
	SamplesUS     map[string][]int64           `json:"samples_us"`
	Cache         map[string]cacheLatencyStats `json:"cache"`
	RuntimeCaches []cachemetrics.Snapshot      `json:"runtime_caches,omitempty"`
	Timeouts      uint64                       `json:"timeouts"`
}

type cacheLatencyStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
}

type latencyRecorder struct {
	events chan latencyEvent
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

type latencyEvent struct {
	kind     uint8
	phase    string
	duration time.Duration
	tier     string
	hit      bool
	reply    chan latencySnapshot
	updated  time.Time
}

const (
	latencyRecordEvent uint8 = iota
	latencyCacheEvent
	latencyTimeoutEvent
	latencySnapshotEvent
	latencyPersistEvent
	latencyPersistedEvent
)

func newLatencyRecorder() *latencyRecorder {
	pid := os.Getpid()
	return newLatencyRecorderWithSession(fmt.Sprintf("%d-%d", pid, time.Now().UnixNano()), pid)
}

func newLatencyRecorderWithSession(sessionID string, pid int) *latencyRecorder {
	recorder := &latencyRecorder{
		events: make(chan latencyEvent, 1024),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go recorder.run(latencySnapshot{
		SessionID: sessionID,
		PID:       pid,
		SamplesUS: make(map[string][]int64),
		Cache:     make(map[string]cacheLatencyStats),
	})
	return recorder
}

func (r *latencyRecorder) record(phase string, duration time.Duration) {
	r.enqueue(latencyEvent{kind: latencyRecordEvent, phase: phase, duration: duration})
}

func (r *latencyRecorder) cache(tier string, hit bool) {
	r.enqueue(latencyEvent{kind: latencyCacheEvent, tier: tier, hit: hit})
}

func (r *latencyRecorder) timeout() {
	r.enqueue(latencyEvent{kind: latencyTimeoutEvent})
}

func (r *latencyRecorder) snapshotCopy() latencySnapshot {
	reply := make(chan latencySnapshot, 1)
	select {
	case r.events <- latencyEvent{kind: latencySnapshotEvent, reply: reply}:
	case <-r.stop:
		return latencySnapshot{}
	}
	select {
	case snapshot := <-reply:
		return snapshot
	case <-r.stop:
		return latencySnapshot{}
	}
}

func (r *latencyRecorder) enqueue(event latencyEvent) {
	select {
	case r.events <- event:
	case <-r.stop:
	default:
		// Telemetry must never add backpressure to interactive suggestions.
	}
}

func (r *latencyRecorder) run(snapshot latencySnapshot) {
	defer close(r.done)
	var lastPersist time.Time
	persisting := false
	scheduled := false
	schedule := func() {
		if persisting || scheduled {
			return
		}
		delay := time.Second - time.Since(lastPersist)
		if lastPersist.IsZero() || delay < 100*time.Millisecond {
			delay = 100 * time.Millisecond
		}
		scheduled = true
		time.AfterFunc(delay, func() { r.enqueue(latencyEvent{kind: latencyPersistEvent}) })
	}
	for {
		var event latencyEvent
		select {
		case event = <-r.events:
		case <-r.stop:
			return
		}
		switch event.kind {
		case latencyRecordEvent:
			samples := append(snapshot.SamplesUS[event.phase], event.duration.Microseconds())
			if len(samples) > latencySampleLimit {
				copy(samples, samples[len(samples)-latencySampleLimit:])
				samples = samples[:latencySampleLimit]
			}
			snapshot.SamplesUS[event.phase] = samples
			snapshot.UpdatedAt = time.Now()
			schedule()
		case latencyCacheEvent:
			stats := snapshot.Cache[event.tier]
			if event.hit {
				stats.Hits++
			} else {
				stats.Misses++
			}
			snapshot.Cache[event.tier] = stats
			snapshot.UpdatedAt = time.Now()
			schedule()
		case latencyTimeoutEvent:
			snapshot.Timeouts++
			snapshot.UpdatedAt = time.Now()
			schedule()
		case latencySnapshotEvent:
			snapshot.RuntimeCaches = cachemetrics.Snapshots()
			event.reply <- cloneLatencySnapshot(snapshot)
		case latencyPersistEvent:
			scheduled = false
			if persisting {
				continue
			}
			persisting = true
			snapshot.RuntimeCaches = cachemetrics.Snapshots()
			copySnapshot := cloneLatencySnapshot(snapshot)
			go func() {
				_ = persistLatencySnapshot(copySnapshot)
				r.enqueue(latencyEvent{kind: latencyPersistedEvent, updated: copySnapshot.UpdatedAt})
			}()
		case latencyPersistedEvent:
			persisting = false
			lastPersist = time.Now()
			if snapshot.UpdatedAt.After(event.updated) {
				schedule()
			}
		}
	}
}

func (r *latencyRecorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	<-r.done
}

func cloneLatencySnapshot(snapshot latencySnapshot) latencySnapshot {
	copySnapshot := snapshot
	copySnapshot.SamplesUS = make(map[string][]int64, len(snapshot.SamplesUS))
	for phase, samples := range snapshot.SamplesUS {
		copySnapshot.SamplesUS[phase] = append([]int64(nil), samples...)
	}
	copySnapshot.Cache = make(map[string]cacheLatencyStats, len(snapshot.Cache))
	for tier, stats := range snapshot.Cache {
		copySnapshot.Cache[tier] = stats
	}
	copySnapshot.RuntimeCaches = append([]cachemetrics.Snapshot(nil), snapshot.RuntimeCaches...)
	return copySnapshot
}

func latencySnapshotPath(sessionID string, pid int) (string, error) {
	dir, err := config.CachePath()
	if err != nil {
		return "", err
	}
	sanitized := sanitizeLatencySessionID(sessionID)
	return filepath.Join(dir, "suggestion-latency-"+sanitized+"-"+strconv.Itoa(pid)+".json"), nil
}

func sanitizeLatencySessionID(sessionID string) string {
	sanitized := regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(sessionID, "-")
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		return "session"
	}
	return sanitized
}

func persistLatencySnapshot(snapshot latencySnapshot) error {
	path, err := latencySnapshotPath(snapshot.SessionID, snapshot.PID)
	if err != nil {
		return err
	}
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := config.WritePrivateFile(temporary, data); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return config.RestrictPrivateFiles(path)
}

func loadLatencySnapshot(sessionID string) (latencySnapshot, error) {
	dir, err := config.CachePath()
	if err != nil {
		return latencySnapshot{}, err
	}
	pattern := "suggestion-latency-*.json"
	if sessionID != "" {
		pattern = "suggestion-latency-" + sanitizeLatencySessionID(sessionID) + "-*.json"
	}
	paths, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return latencySnapshot{}, err
	}
	if len(paths) == 0 && sessionID == "" {
		legacy := filepath.Join(dir, "suggestion-latency.json")
		paths = append(paths, legacy)
	} else if len(paths) == 0 {
		return latencySnapshot{}, os.ErrNotExist
	}
	sort.Slice(paths, func(i, j int) bool {
		left, leftErr := os.Stat(paths[i])
		right, rightErr := os.Stat(paths[j])
		if leftErr != nil {
			return false
		}
		if rightErr != nil {
			return true
		}
		return left.ModTime().After(right.ModTime())
	})
	path := paths[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return latencySnapshot{}, err
	}
	var snapshot latencySnapshot
	err = json.Unmarshal(data, &snapshot)
	if snapshot.Cache == nil {
		snapshot.Cache = make(map[string]cacheLatencyStats)
	}
	return snapshot, err
}

func latencyPercentile(samples []int64, percentile float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	ordered := append([]int64(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered)-1)*percentile + 0.5)
	return time.Duration(ordered[index]) * time.Microsecond
}

var suggestionLatency = newLatencyRecorder()

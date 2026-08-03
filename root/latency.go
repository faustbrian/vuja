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

	"github.com/faustbrian/vuja/internal/config"
)

const latencySampleLimit = 256

type latencySnapshot struct {
	UpdatedAt time.Time                    `json:"updated_at"`
	SessionID string                       `json:"session_id"`
	PID       int                          `json:"pid"`
	SamplesUS map[string][]int64           `json:"samples_us"`
	Cache     map[string]cacheLatencyStats `json:"cache"`
	Timeouts  uint64                       `json:"timeouts"`
}

type cacheLatencyStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
}

type latencyRecorder struct {
	mu          sync.Mutex
	snapshot    latencySnapshot
	lastPersist time.Time
	persisting  bool
}

func newLatencyRecorder() *latencyRecorder {
	pid := os.Getpid()
	return newLatencyRecorderWithSession(fmt.Sprintf("%d-%d", pid, time.Now().UnixNano()), pid)
}

func newLatencyRecorderWithSession(sessionID string, pid int) *latencyRecorder {
	return &latencyRecorder{snapshot: latencySnapshot{
		SessionID: sessionID,
		PID:       pid,
		SamplesUS: make(map[string][]int64),
		Cache:     make(map[string]cacheLatencyStats),
	}}
}

func (r *latencyRecorder) record(phase string, duration time.Duration) {
	r.mu.Lock()
	samples := append(r.snapshot.SamplesUS[phase], duration.Microseconds())
	if len(samples) > latencySampleLimit {
		samples = append([]int64(nil), samples[len(samples)-latencySampleLimit:]...)
	}
	r.snapshot.SamplesUS[phase] = samples
	r.snapshot.UpdatedAt = time.Now()
	r.maybePersistLocked()
	r.mu.Unlock()
}

func (r *latencyRecorder) cache(tier string, hit bool) {
	r.mu.Lock()
	stats := r.snapshot.Cache[tier]
	if hit {
		stats.Hits++
	} else {
		stats.Misses++
	}
	r.snapshot.Cache[tier] = stats
	r.snapshot.UpdatedAt = time.Now()
	r.maybePersistLocked()
	r.mu.Unlock()
}

func (r *latencyRecorder) timeout() {
	r.mu.Lock()
	r.snapshot.Timeouts++
	r.snapshot.UpdatedAt = time.Now()
	r.maybePersistLocked()
	r.mu.Unlock()
}

func (r *latencyRecorder) snapshotCopy() latencySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneLatencySnapshot(r.snapshot)
}

func (r *latencyRecorder) maybePersistLocked() {
	if r.persisting {
		return
	}
	delay := time.Second - time.Since(r.lastPersist)
	if r.lastPersist.IsZero() || delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	r.persisting = true
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		r.mu.Lock()
		snapshot := cloneLatencySnapshot(r.snapshot)
		r.lastPersist = time.Now()
		r.mu.Unlock()
		_ = persistLatencySnapshot(snapshot)
		r.mu.Lock()
		dirty := r.snapshot.UpdatedAt.After(snapshot.UpdatedAt)
		r.persisting = false
		if dirty {
			r.maybePersistLocked()
		}
		r.mu.Unlock()
	}()
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

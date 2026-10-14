package root

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"golang.org/x/sys/unix"
)

const outputRecoveryByteLimit = 1024 * 1024
const outputRecoveryPaneLimit = 64

type outputRecoveryState struct {
	Version int       `json:"version"`
	Owner   string    `json:"owner"`
	Rows    []string  `json:"rows"`
	SavedAt time.Time `json:"saved_at,omitempty"`
}

type outputRecovery struct {
	dir, path, owner string
	rows             []string
	linkFor          func(string) string
	restored         bool
	failed           bool
	superseded       bool
	writer           *outputCheckpointWriter
	dirty            bool
	nextCheckpoint   time.Time
	maxAge           time.Duration
	maxPanes         int
}

// iTerm's positional prefix changes when panes move; its arrangement GUID does
// not. Never infer pane ownership from cwd, PID, or another pane's last output.
func outputRecoveryPaneID(getenv func(string) string) string {
	// tmux panes inherit the outer terminal UUID, not a reboot-stable identity
	// of their own. Sharing it would replay another pane's private output.
	if getenv("TMUX") != "" {
		return ""
	}
	for _, key := range []string{"ITERM_SESSION_ID", "TERM_SESSION_ID"} {
		value := getenv(key)
		if _, suffix, ok := strings.Cut(value, ":"); ok {
			value = suffix
		}
		if codexResumeIDPattern.FindString(value) == value && value != "" {
			return strings.ToLower(value)
		}
	}
	return ""
}

// Recovery is explicitly configured before owned output starts. Constructors
// and renderer previews never access storage implicitly.
func (c *terminalCompositor) ConfigureOutputRecovery(dir, pane string, linkFor func(string) string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("cannot configure output recovery after close")
	}
	if c.outputRecovery != nil {
		return errors.New("output recovery is already configured for this session")
	}
	if pane == "" {
		return nil
	}
	if c.chatboxConfig.OutputCheckpoints == "off" {
		return fenceDisabledOutputRecovery(dir, pane)
	}
	if !c.enabled || c.chatboxConfig.OutputViewport != "pinned" {
		return nil
	}
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe output recovery directory")
	}
	if err := config.EnsurePrivateDir(dir); err != nil {
		return err
	}
	key := sha256.Sum256([]byte(pane))
	var owner [16]byte
	if _, err := rand.Read(owner[:]); err != nil {
		return err
	}
	r := &outputRecovery{dir: dir, path: filepath.Join(dir, hex.EncodeToString(key[:])+".json"), owner: hex.EncodeToString(owner[:]), linkFor: linkFor,
		maxAge: c.chatboxConfig.OutputCheckpointMaxAge, maxPanes: c.chatboxConfig.OutputCheckpointPanes}
	if r.maxPanes == 0 {
		r.maxPanes = outputRecoveryPaneLimit
	}
	now := c.checkpointNow()
	if err := r.locked(func() error {
		state, err := r.read()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if checkpointExpired(state.SavedAt, now, r.maxAge) {
			state.Rows, state.SavedAt = nil, time.Time{}
		}
		r.rows = state.Rows
		state.Version, state.Owner = 1, r.owner
		if err := r.writeState(state); err != nil {
			return err
		}
		return r.prune(now)
	}); err != nil {
		return err
	}
	c.outputRecovery = r
	r.writer = newOutputCheckpointWriter(func(snapshot outputCheckpointSnapshot) {
		defer c.containVisualFailure()
		c.mu.Lock()
		allowed := c.outputRecovery == r && c.chatboxConfig.OutputCheckpoints != "off" && c.chatboxConfig.OutputViewport == "pinned" && !r.superseded
		c.mu.Unlock()
		if !allowed {
			return
		}
		superseded, err := r.persist(snapshot)
		c.mu.Lock()
		defer c.mu.Unlock()
		r.superseded = r.superseded || superseded
		if c.closed || c.outputRecovery != r || c.chatboxConfig.OutputCheckpoints == "off" {
			return
		}
		if err != nil && !r.superseded {
			// A failed write must not consume quiet output's eligibility. Keep
			// the existing interval deadline so retries remain bounded.
			r.dirty = true
		}
		if err != nil && !r.failed {
			r.failed = true
			if c.viewport != nil {
				c.viewport.write([]byte("\r\n[VUJA] output recovery unavailable; live output is unaffected\r\n"))
				c.viewport.dirty = true
				c.renderOutputViewport()
			}
		}
	})
	return nil
}

// Disabling capture does not erase prior saved output, but it must revoke an
// older instance's publishing authority even when this session uses the native
// terminal viewport. Missing storage stays missing; no capture worker starts.
func fenceDisabledOutputRecovery(dir, pane string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe output recovery directory")
	}
	r := &outputRecovery{dir: dir, path: filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(pane))))}
	return r.withLock(func() error {
		state, err := r.read()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		// Enabled owners are random nonempty IDs. An empty owner is a
		// disabled fence, not permission for any existing writer to reclaim it.
		state.Owner = ""
		return r.writeState(state)
	})
}

func (r *outputRecovery) locked(fn func() error) error {
	return r.withLock(func() error {
		if err := r.removeInterruptedWrites(); err != nil {
			return err
		}
		return fn()
	})
}

func (r *outputRecovery) withLock(fn func() error) error {
	fd, err := unix.Open(filepath.Join(r.dir, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "output recovery lock")
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	// Concurrently restored panes can checkpoint together. Bound lock waiting
	// rather than disabling recovery on the first momentary collision.
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn()
}

// CreateTemp uses a decimal suffix. Under the shared lock no checkpoint writer
// is active, so leftover regular temporary files belong to interrupted writes.
func (r *outputRecovery) removeInterruptedWrites() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !isInterruptedCheckpointName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := os.Remove(filepath.Join(r.dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func isInterruptedCheckpointName(name string) bool {
	suffix, ok := strings.CutPrefix(name, ".checkpoint-")
	return ok && suffix != "" && len(suffix) <= 10 && strings.IndexFunc(suffix, func(c rune) bool { return c < '0' || c > '9' }) < 0
}

func (r *outputRecovery) read() (outputRecoveryState, error) {
	return readOutputRecoveryState(r.path)
}

func readOutputRecoveryState(path string) (outputRecoveryState, error) {
	var state outputRecoveryState
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return state, err
	}
	f := os.NewFile(uintptr(fd), "output recovery")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > outputRecoveryByteLimit {
		return state, errors.New("invalid output recovery size")
	}
	if err := f.Chmod(0o600); err != nil {
		return state, err
	}
	data, err := io.ReadAll(io.LimitReader(f, outputRecoveryByteLimit+1))
	if err != nil {
		return state, err
	}
	if len(data) > outputRecoveryByteLimit || json.Unmarshal(data, &state) != nil || state.Version != 1 || len(state.Rows) > 10000 {
		return outputRecoveryState{}, errors.New("invalid output recovery state")
	}
	for i, row := range state.Rows {
		state.Rows[i] = recoveryRow(row, nil, false)
	}
	if state.SavedAt.IsZero() && len(state.Rows) > 0 {
		state.SavedAt = info.ModTime()
	}
	return state, nil
}

func (r *outputRecovery) write(rows []string) error {
	return r.writeState(outputRecoveryState{Version: 1, Owner: r.owner, Rows: rows, SavedAt: time.Now().UTC()})
}

func (r *outputRecovery) writeState(state outputRecoveryState) error {
	rows := state.Rows
	// Account for JSON escaping once per row rather than repeatedly encoding a
	// shrinking large transcript. Serialization work is linear in retained data.
	budget := outputRecoveryByteLimit - 512
	start := len(rows)
	for start > 0 {
		encoded, err := json.Marshal(rows[start-1])
		if err != nil {
			return err
		}
		if len(encoded)+1 > budget {
			break
		}
		budget -= len(encoded) + 1
		start--
	}
	rows = rows[start:]
	state.Rows = rows
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.dir, ".checkpoint-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), r.path); err != nil {
		return err
	}
	dir, err := os.Open(r.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func checkpointExpired(savedAt, now time.Time, maxAge time.Duration) bool {
	return maxAge > 0 && !savedAt.IsZero() && savedAt.Before(now.Add(-maxAge))
}

func (r *outputRecovery) prune(now time.Time) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	type checkpoint struct {
		name string
		info os.FileInfo
	}
	var checkpoints []checkpoint
	for _, e := range entries {
		if filepath.Join(r.dir, e.Name()) == r.path {
			continue
		}
		if len(e.Name()) != 69 || !strings.HasSuffix(e.Name(), ".json") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(e.Name(), ".json")); err != nil {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			if r.maxAge > 0 {
				state, readErr := readOutputRecoveryState(filepath.Join(r.dir, e.Name()))
				if readErr == nil && checkpointExpired(state.SavedAt, now, r.maxAge) {
					if err := os.Remove(filepath.Join(r.dir, e.Name())); err != nil {
						return err
					}
					continue
				}
			}
			checkpoints = append(checkpoints, checkpoint{e.Name(), info})
		}
	}
	slices.SortFunc(checkpoints, func(a, b checkpoint) int {
		if cmp := a.info.ModTime().Compare(b.info.ModTime()); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.name, b.name)
	})
	for len(checkpoints) > clamp(r.maxPanes, 1, outputRecoveryPaneLimit)-1 {
		old := checkpoints[0]
		checkpoints = checkpoints[1:]
		if err := os.Remove(filepath.Join(r.dir, old.name)); err != nil {
			return err
		}
	}
	return nil
}

func (c *terminalCompositor) restoreOutputRecovery() {
	r := c.outputRecovery
	if r == nil || r.restored {
		return
	}
	r.restored = true
	if len(r.rows) > 0 {
		c.viewport.write([]byte("[VUJA] Output restored; process not resumed\r\n"))
	}
	for i, row := range r.rows {
		if i > 0 {
			c.viewport.write([]byte("\r\n"))
		}
		c.viewport.write([]byte(recoveryRow(row, r.linkFor, true)))
	}
	r.rows = nil
}

// Snapshot under compositor ownership; persistence runs only on its bounded
// writer. Close drains queued completed output outside the rendering lock.
func (c *terminalCompositor) checkpointOutputRecovery() {
	r := c.outputRecovery
	if r == nil || r.writer == nil || c.viewport == nil || !r.restored || r.superseded ||
		c.chatboxConfig.OutputCheckpoints == "off" || c.chatboxConfig.OutputViewport != "pinned" {
		return
	}
	m := c.viewport.model
	count := m.ScrollbackLen() + m.CursorPosition().Y + 1
	var rows []string
	bytes := 0
	for y := count - 1; y >= max(0, count-min(clamp(c.chatboxConfig.OutputLines, 1, 10000)+m.Height(), 10000)); y-- {
		history := y < m.ScrollbackLen()
		row := y
		if !history {
			row -= m.ScrollbackLen()
		}
		line := recoveryRow(viewportLine(m, row, c.width, history), nil, false)
		bytes += len(line)
		if bytes > outputRecoveryByteLimit {
			break
		}
		rows = append(rows, line)
	}
	slices.Reverse(rows)
	r.dirty = false
	now := c.checkpointNow()
	r.nextCheckpoint = now.Add(c.outputCheckpointInterval())
	r.writer.submit(outputCheckpointSnapshot{rows: rows, savedAt: now.UTC()})
}

func (c *terminalCompositor) checkpointNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *terminalCompositor) outputCheckpointInterval() time.Duration {
	if c.chatboxConfig.OutputCheckpointInterval < time.Second {
		return 5 * time.Second
	}
	return c.chatboxConfig.OutputCheckpointInterval
}

// The existing running-command scheduler owns the clock. No second timer or
// polling worker is needed, and native applications remain outside capture.
func (c *terminalCompositor) checkpointRunningOutput() {
	r := c.outputRecovery
	if r == nil || !r.dirty || c.chatboxConfig.OutputCheckpoints != "running" ||
		!c.outputViewportEnabled() || !c.viewportCommand || c.commandStartedAt.IsZero() {
		return
	}
	due := c.commandStartedAt.Add(c.outputCheckpointInterval())
	if r.nextCheckpoint.After(due) {
		due = r.nextCheckpoint
	}
	if !c.checkpointNow().Before(due) {
		c.checkpointOutputRecovery()
	}
}

func (r *outputRecovery) persist(snapshot outputCheckpointSnapshot) (superseded bool, err error) {
	err = r.locked(func() error {
		state, err := r.read()
		if errors.Is(err, os.ErrNotExist) {
			// A writer owns only the state it explicitly claimed. Clearing or
			// expiring that state must not allow a queued old write to recreate it.
			superseded = true
			return nil
		}
		if err != nil {
			return err
		}
		if err == nil && state.Owner != r.owner {
			superseded = true
			return nil
		}
		if err := r.writeState(outputRecoveryState{Version: 1, Owner: r.owner, Rows: snapshot.rows, SavedAt: snapshot.savedAt}); err != nil {
			return err
		}
		return r.prune(snapshot.savedAt)
	})
	return superseded, err
}

// Disk rows contain only text, SGR and credential-free observed Codex links.
// Never replay cursor controls, notifications, OSC52 clipboard writes or old
// process capabilities. Rebinding also works across a UUID's wrapped rows.
func recoveryRow(row string, linkFor func(string) string, restore bool) string {
	var out strings.Builder
	for i := 0; i < len(row); {
		if row[i] != '\x1b' {
			if row[i] >= 0x20 && row[i] != 0x7f {
				out.WriteByte(row[i])
			}
			i++
			continue
		}
		if i+1 < len(row) && row[i+1] == '[' {
			end := i + 2
			validSGR := true
			for end < len(row) && row[end] >= 0x20 && row[end] <= 0x3f {
				validSGR = validSGR && (row[end] >= '0' && row[end] <= '9' || row[end] == ';' || row[end] == ':')
				end++
			}
			if end < len(row) && row[end] >= 0x40 && row[end] <= 0x7e {
				if row[end] == 'm' && validSGR {
					out.WriteString(row[i : end+1])
				}
				i = end + 1
				continue
			}
		}
		if i+1 < len(row) && row[i+1] == ']' {
			end, n := oscEnd([]byte(row), i+2)
			if end < 0 {
				break
			}
			payload := row[i+2 : end]
			if rest, ok := strings.CutPrefix(payload, "8;"); ok {
				_, target, ok := strings.Cut(rest, ";")
				if ok {
					if target == "" {
						out.WriteString("\x1b]8;;\x1b\\")
					} else if id := recoveryResumeID(target); id != "" {
						target = "vuja://codex-resume/" + id
						if restore {
							target = ""
							if linkFor != nil {
								target = linkFor(id)
							}
						}
						if target != "" {
							fmt.Fprintf(&out, "\x1b]8;;%s\x1b\\", target)
						}
					}
				}
			}
			i = end + n
			continue
		}
		i++
	}
	return out.String()
}

func recoveryResumeID(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "vuja" || u.Host != "codex-resume" {
		return ""
	}
	id := strings.TrimPrefix(u.Path, "/")
	if id == "" || codexResumeIDPattern.FindString(id) != id {
		return ""
	}
	return strings.ToLower(id)
}

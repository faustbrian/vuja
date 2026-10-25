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
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/v2/internal/config"
	"golang.org/x/sys/unix"
)

const outputRecoveryByteLimit = 1024 * 1024
const outputRecoveryPaneLimit = 64
const outputRecoveryTruncated = "[VUJA] checkpoint output truncated"

type outputRecoveryState struct {
	Version   int                   `json:"version"`
	Owner     string                `json:"owner"`
	Rows      []string              `json:"rows"`
	Blocks    []checkpointBlockSpan `json:"block_states,omitempty"`
	SavedAt   time.Time             `json:"saved_at,omitempty"`
	Truncated bool                  `json:"truncated,omitempty"`
}

type outputRecovery struct {
	dir, path, owner string
	rows             []string
	blocks           []checkpointBlockSpan
	truncated        bool
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
	c.resumeLinkFor = linkFor
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
			state.Blocks = nil
			state.Truncated = false
		}
		r.rows = state.Rows
		r.blocks = state.Blocks
		r.truncated = state.Truncated
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
				c.writeViewportNotification([]byte("\r\n[VUJA] output recovery unavailable; live output is unaffected\r\n"))
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
	// Decode cosmetic data separately: an incompatible optional presentation
	// shape must not make otherwise valid version-1 transcript text unreadable.
	type recoveryFields outputRecoveryState
	decoded := struct {
		*recoveryFields
		Blocks json.RawMessage `json:"block_states"`
	}{recoveryFields: (*recoveryFields)(&state)}
	if len(data) > outputRecoveryByteLimit || json.Unmarshal(data, &decoded) != nil || state.Version != 1 || len(state.Rows) > 10000 {
		return outputRecoveryState{}, errors.New("invalid output recovery state")
	}
	if json.Unmarshal(decoded.Blocks, &state.Blocks) != nil {
		state.Blocks = nil
	}
	for i, row := range state.Rows {
		state.Rows[i] = recoveryRow(row, nil, false)
	}
	// Optional display metadata never determines which text can be restored.
	// Old checkpoints have none; invalid/future values restore neutral text.
	last := -1
	for i, b := range state.Blocks {
		if b.First <= last || b.Last < b.First || b.Last >= len(state.Rows) || b.State < blockNeutral || b.State > blockError {
			state.Blocks = nil
			break
		}
		last = b.Last
		// Cursor-addressed output or stale cosmetic metadata may replace the
		// original row. Origin alone must never hide that replacement text.
		state.Blocks[i].Divider = b.Divider && isOutputBlockDivider(state.Rows[b.First])
		if card := b.Card; card != nil && (card.BodyFirst < b.First || card.BodyEnd < card.BodyFirst || card.BodyEnd > b.Last+1 ||
			card.OutcomeFirst < 0 || card.OutcomeEnd < card.OutcomeFirst || card.OutcomeEnd > b.Last+1 || card.OutcomeEnd > 0 && card.OutcomeFirst < card.BodyEnd) {
			state.Blocks[i].Card = nil
		}
		if card := state.Blocks[i].Card; card != nil {
			previous := card.BodyFirst - 1
			for _, notice := range card.Notices {
				if !card.PaneOnly || notice.First <= previous || notice.First < card.BodyFirst || notice.Last < notice.First || notice.Last >= card.BodyEnd {
					state.Blocks[i].Card = nil
					break
				}
				previous = notice.Last
			}
		}
		if card := state.Blocks[i].Card; card != nil && card.BodyFirst > b.First && isOutputBlockDivider(state.Rows[b.First]) {
			// Older card metadata identifies the compositor-owned header even
			// without the divider flag. Never infer origin from a program body.
			state.Blocks[i].Divider = true
		}
	}
	// Early version-1 checkpoints carried only this reserved leading notice.
	// Lift it into metadata before a smaller live model can evict the row.
	state.Truncated = state.Truncated || len(state.Rows) > 0 && state.Rows[0] == outputRecoveryTruncated
	if state.SavedAt.IsZero() && len(state.Rows) > 0 {
		state.SavedAt = info.ModTime()
	}
	return state, nil
}

func (r *outputRecovery) write(rows []string) error {
	return r.writeState(outputRecoveryState{Version: 1, Owner: r.owner, Rows: rows, SavedAt: time.Now().UTC()})
}

func (r *outputRecovery) writeState(state outputRecoveryState) error {
	// The disk cap counts escaped JSON, not logical text or screen columns.
	// Prefer dropping styling to text; if text must go, keep the newest suffix
	// and reserve room for a notice that survives legacy version-1 readers.
	const budget = outputRecoveryByteLimit - 512
	rows, shortened := checkpointRowsWithinBudget(state.Rows, budget, 10000)
	blocks := checkpointBlocksForRows(state.Blocks, state.Rows, rows)
	state.Truncated = state.Truncated || shortened
	if state.Truncated {
		original := state.Rows
		originalBlocks := state.Blocks
		if len(original) > 0 && original[0] == outputRecoveryTruncated {
			original = original[1:]
			originalBlocks = checkpointBlockSuffix(originalBlocks, 1, len(original))
		}
		notice, _ := json.Marshal(outputRecoveryTruncated)
		rows, _ = checkpointRowsWithinBudget(original, budget-len(notice)-1, 9999)
		blocks = checkpointBlocksForRows(originalBlocks, original, rows)
		rows = append([]string{outputRecoveryTruncated}, rows...)
		for i := range blocks {
			blocks[i].First++
			blocks[i].Last++
			if card := blocks[i].Card; card != nil {
				card.BodyFirst++
				card.BodyEnd++
				if card.OutcomeEnd > 0 {
					card.OutcomeFirst++
					card.OutcomeEnd++
				}
				for j := range card.Notices {
					card.Notices[j].First++
					card.Notices[j].Last++
				}
			}
		}
	}
	state.Rows = rows
	state.Blocks = blocks
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > outputRecoveryByteLimit && len(state.Blocks) > 0 {
		// Cosmetic state must never evict otherwise fitting transcript text.
		state.Blocks = nil
		data, err = json.Marshal(state)
		if err != nil {
			return err
		}
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

// Each sizing pass encodes a row at most twice. Styled rows fall back to plain
// text, and one backward rune scan bounds the final suffix without repeatedly
// encoding progressively smaller copies of a large logical line.
func checkpointRowsWithinBudget(rows []string, budget, maxRows int) ([]string, bool) {
	kept := make([]string, 0, min(len(rows), maxRows))
	shortened := false
	for y := len(rows) - 1; y >= 0; y-- {
		if len(kept) == maxRows {
			shortened = true
			break
		}
		row := rows[y]
		encoded, _ := json.Marshal(row)
		if len(encoded)+1 > budget {
			row = ansi.Strip(row)
			encoded, _ = json.Marshal(row)
		}
		if len(encoded)+1 > budget {
			if suffix := checkpointStringSuffix(row, budget-1); suffix != "" {
				kept = append(kept, suffix)
			}
			shortened = true
			break
		}
		budget -= len(encoded) + 1
		kept = append(kept, row)
	}
	slices.Reverse(kept)
	return kept, shortened
}

// Match encoding/json's string size, including HTML escaping and the two
// quotes. Starting at a rune boundary keeps a shortened checkpoint valid UTF-8.
func checkpointStringSuffix(text string, budget int) string {
	start, size := len(text), 2
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:start])
		encoded := n
		switch r {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			encoded = 2
		case '<', '>', '&', '\u2028', '\u2029':
			encoded = 6
		default:
			if r < 0x20 || r == utf8.RuneError && n == 1 {
				encoded = 6
			}
		}
		if size+encoded > budget {
			break
		}
		start -= n
		size += encoded
	}
	return strings.Clone(text[start:])
}

func (c *terminalCompositor) restoreOutputRecovery() {
	r := c.outputRecovery
	if r == nil || r.restored {
		return
	}
	r.restored = true
	c.viewport.restoredTruncated = r.truncated
	if len(r.rows) > 0 {
		c.viewport.write([]byte("[VUJA] Output restored; process not resumed\r\n"))
	}
	base := c.viewport.cursorLogicalRow()
	for i, row := range r.rows {
		if i > 0 {
			c.viewport.write([]byte("\r\n"))
		}
		c.viewport.write([]byte(recoveryRow(row, r.linkFor, true)))
	}
	for _, b := range r.blocks {
		c.viewport.blocks = append(c.viewport.blocks, blockSpan{first: base + uint64(b.First), last: base + uint64(b.Last), state: b.State, divider: b.Divider})
		if stored := b.Card; stored != nil {
			// Early mixed-pane metadata had no attribution ranges. Keep those
			// rows in the uncapped text view, rather than hiding unknown notices.
			if stored.PaneOnly && len(stored.Notices) == 0 {
				continue
			}
			card := outputCard{first: base + uint64(b.First), last: base + uint64(b.Last), bodyFirst: base + uint64(stored.BodyFirst), bodyEnd: base + uint64(stored.BodyEnd), state: b.State, newline: stored.Newline, native: stored.Native, truncated: stored.Truncated, paneOnly: stored.PaneOnly, divider: b.Divider}
			for _, notice := range stored.Notices {
				card.notices = append(card.notices, blockSpan{first: base + uint64(notice.First), last: base + uint64(notice.Last)})
			}
			if stored.OutcomeEnd > 0 {
				card.outcomeFirst, card.outcomeEnd = base+uint64(stored.OutcomeFirst), base+uint64(stored.OutcomeEnd)
				card.outcome = slices.Clone(r.rows[stored.OutcomeFirst:stored.OutcomeEnd])
			}
			c.viewport.cards.completed = append(c.viewport.cards.completed, card)
		}
	}
	r.rows = nil
	r.blocks = nil
	c.viewport.pruneBlockStates()
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
	stored := retainedModelRows(m)
	storedBlocks := c.viewport.checkpointBlocks(len(stored))
	count := len(stored)
	var rows []string
	bytes := 0
	first := max(0, count-min(clamp(c.chatboxConfig.OutputLines, 1, 10000)+m.Height(), 10000))
	shortened := first > 0 || c.viewport.outputTruncated()
	for y := count - 1; y >= first; y-- {
		line := recoveryRow(stored[y], nil, false)
		remaining := outputRecoveryByteLimit - bytes
		if len(line) > remaining {
			line = ansi.Strip(line)
		}
		if len(line) > remaining {
			if suffix := checkpointStringSuffix(line, remaining); suffix != "" {
				rows = append(rows, suffix)
			}
			shortened = true
			break
		}
		bytes += len(line)
		rows = append(rows, line)
	}
	slices.Reverse(rows)
	blocks := checkpointBlocksForRows(storedBlocks, stored, rows)
	r.dirty = false
	now := c.checkpointNow()
	r.nextCheckpoint = now.Add(c.outputCheckpointInterval())
	r.writer.submit(outputCheckpointSnapshot{rows: rows, blocks: blocks, savedAt: now.UTC(), truncated: shortened})
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
		if state.Owner != r.owner {
			superseded = true
			return nil
		}
		if err := r.writeState(outputRecoveryState{Version: 1, Owner: r.owner, Rows: snapshot.rows, Blocks: snapshot.blocks, SavedAt: snapshot.savedAt, Truncated: snapshot.truncated}); err != nil {
			return err
		}
		return r.prune(snapshot.savedAt)
	})
	return superseded, err
}

// Disk rows contain only text, SGR, non-action divider metadata and
// credential-free observed Codex links.
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
					} else if target == outputBlockDividerTarget {
						// This fixed non-action URI can occur in application output
						// too. Preserve its text/style through normal conversion;
						// only the separate row provenance can establish chrome.
						fmt.Fprintf(&out, "\x1b]8;;%s\x1b\\", target)
					} else if parsed, err := url.Parse(target); err == nil {
						resume, valid := resumeTargetFromURL(parsed)
						if !valid {
							i = end + n
							continue
						}
						target = resume.uri().String()
						if restore {
							target = ""
							if linkFor != nil {
								target = linkFor(resume.value())
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

package root

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/faustbrian/vuja/v2/internal/config"
)

// Explicit exports are not checkpoints. They are private, user-created files;
// no output is sent to disk merely by opening search or execution browsing.
func writeRetainedOutputExport(rows retainedExecution) (path string, err error) {
	if rows.applicationOwned {
		return "", errors.New("native application output is not captured")
	}
	cache, err := config.CachePath()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "exports")
	for _, private := range []string{cache, dir} {
		if info, statErr := os.Lstat(private); statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", errors.New("output export path is not a private directory")
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		if err := config.EnsurePrivateDir(private); err != nil {
			return "", err
		}
	}
	rows.trim(10000, retainedExecutionBytes)
	text := rows.plainText()
	if rows.truncated {
		text = "[VUJA: retained output truncated]\n" + text
	}
	f, err := os.CreateTemp(dir, ".output-*")
	if err != nil {
		return "", err
	}
	temporary := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(temporary)
	}()
	if err = f.Chmod(config.PrivateFileMode); err != nil {
		return "", err
	}
	if n, writeErr := io.WriteString(f, text); writeErr != nil {
		return "", writeErr
	} else if n != len(text) {
		return "", io.ErrShortWrite
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	path = filepath.Join(dir, strings.TrimPrefix(filepath.Base(temporary), ".")+".txt")
	if err = os.Rename(temporary, path); err != nil {
		return "", err
	}
	return path, nil
}

// Called under compositor ownership. One asynchronous export at a time bounds
// both queued output and workers; disk operations never hold the rendering lock.
func (c *terminalCompositor) exportRetainedOutput() {
	v := c.viewport
	if c.outputExportDone != nil {
		select {
		case <-c.outputExportDone:
		default:
			v.executions.notice = "Output export already running"
			return
		}
	}
	r := retainedExecution{newline: true}
	if v.executions.selected != nil {
		r = *v.executions.selected
	} else if reading := v.cards.reading; reading != nil {
		r.rows, r.truncated = withoutPaneDividers(reading.rows, reading.dividers), reading.truncated
	} else {
		c.freezeOutputViewport()
		r.rows = withoutPaneDividers(v.frozen, v.frozenDividers)
		r.truncated = v.frozenTruncated
	}
	c.exportOutputSnapshot(r)
}

// Every entry point supplies an immutable semantic target, never a screen crop.
func (c *terminalCompositor) exportOutputSnapshot(r retainedExecution) {
	v := c.viewport
	if c.outputExportDone != nil {
		select {
		case <-c.outputExportDone:
		default:
			v.executions.notice = "Output export already running"
			return
		}
	}
	r.rows = append([]string(nil), r.rows...)
	target := "pane"
	if r.id != 0 {
		target = fmt.Sprintf("#%d", r.id)
	}
	v.executions.notice = "Exporting " + target
	done := make(chan struct{})
	c.outputExportDone = done
	go func() {
		defer close(done)
		defer c.containVisualFailure()
		path, err := writeRetainedOutputExport(r)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.viewport != v {
			return
		}
		if err != nil {
			// Raw errors may contain identifying paths or output. The finite
			// status is actionable without leaking them into diagnostic output.
			v.executions.notice = "Export " + target + " failed · check private cache permissions"
		} else {
			v.executions.notice = "Export " + target + ": " + filepath.Base(path)
		}
		v.dirty = true
		c.renderOutputViewport()
	}()
}

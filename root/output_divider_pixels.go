package root

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type pixelBoundary struct{ row, width int }

// A single transient image supplies all visible boundaries. Kitty's I allocation
// returns a terminal-owned ID: guessing an i would overwrite another program's
// image. See https://sw.kovidgoyal.net/kitty/graphics-protocol/ (image IDs/layout).
// Graphics never pass through the transcript, shadow terminal or checkpoint.
type pixelDividers struct {
	enabled                   bool
	epoch, probed, probeEpoch uint64
	screen, uploadScreen      uint64
	probe                     uint32
	probeOK                   bool
	cellWidth, cellHeight     int
	image, upload             uint32
	uploadEpoch               uint64
	width, uploadWidth        int
	color, uploadColor        [3]byte
	placements                []pixelBoundary
	overlay                   bool
}

func (c *terminalCompositor) EnablePixelDividers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dividerPixels.enabled = true
	if c.viewport != nil {
		c.viewport.dirty = true
		c.renderOutputViewport()
	}
}

func dividerNonce() uint32 {
	var data [4]byte
	if _, err := rand.Read(data[:]); err != nil {
		return 0 // No guessed/shared image ID on an entropy failure.
	}
	return binary.BigEndian.Uint32(data[:])
}

func (c *terminalCompositor) deleteDividerImage(free bool) {
	d := &c.dividerPixels
	if d.image != 0 && (free || len(d.placements) > 0) {
		kind := "i"
		if free {
			kind = "I"
		}
		fmt.Fprintf(c.out, "\x1b_Ga=d,d=%s,i=%d,q=2\x1b\\", kind, d.image)
	}
	d.placements = nil
	if free {
		d.image = 0
	}
}

func (c *terminalCompositor) invalidatePixelDividers() {
	d := &c.dividerPixels
	if !d.enabled {
		return
	}
	c.deleteDividerImage(true)
	d.epoch++
	d.cellWidth, d.cellHeight = 0, 0
	// At most one outstanding probe and upload, even on unsupported terminals.
	// Their tagged replies remain ours across resize or native handoff.
}

func (c *terminalCompositor) dividerEligible() bool {
	return c.dividerPixels.enabled && !c.closed && c.viewportAlternate &&
		c.outputViewportEnabled() && c.cardLayoutEnabled() && c.chatboxConfig.BlockDividers
}

func dividerGray(value string) [3]byte {
	gray := byte(70)
	if color := ansi.XParseColor(value); color != nil {
		r, g, b, _ := color.RGBA()
		gray = byte((r + g + b) / (3 * 257))
	}
	return [3]byte{gray, gray, gray}
}

func (c *terminalCompositor) renderPixelDividers(rows []cardRow, cards bool) {
	d := &c.dividerPixels
	if !c.dividerEligible() {
		c.deleteDividerImage(true)
		return
	}
	if !cards || c.transientUIVisible != nil && c.transientUIVisible() {
		c.deleteDividerImage(false)
		return
	}
	if d.probed != d.epoch+1 {
		if d.probe == 0 {
			d.probe = dividerNonce()
			if d.probe == 0 {
				d.probed = d.epoch + 1
				return
			}
			d.probeEpoch, d.probeOK = d.epoch, false
			// Non-storing capability query, cell pixels, then an ordered DA1
			// fence. Missing replies mean no graphic, never a padded glyph row.
			// CSI 16 t / CSI 6;h;w t: xterm ctlseqs, Window manipulation.
			fmt.Fprintf(c.out, "\x1b_Ga=q,i=%d,f=24,s=1,v=1;AAAA\x1b\\\x1b[16t\x1b[c", d.probe)
		}
		return
	}
	if !d.probeOK || d.cellWidth <= 0 || d.cellHeight <= 0 || c.width <= 0 ||
		c.width > terminalWriterMaxPending/3/d.cellWidth {
		// Geometry replies are untrusted. Limit the one-row RGB allocation to
		// the existing terminal writer's byte budget; don't silently crop it.
		c.deleteDividerImage(true)
		return
	}
	width, color := c.width*d.cellWidth, dividerGray(c.inputBoxTheme.Muted)
	if d.image != 0 && (d.width != width || d.color != color) {
		c.deleteDividerImage(true)
	}
	var placements []pixelBoundary
	for y, row := range rows {
		if row.boundary && y < c.viewportContentRows() {
			placements = append(placements, pixelBoundary{y, min(row.width, c.width) * d.cellWidth})
		}
	}
	if len(placements) == 0 {
		c.deleteDividerImage(true)
		return
	}
	if d.image == 0 {
		if d.upload == 0 {
			d.upload = dividerNonce()
			if d.upload == 0 {
				return
			}
			d.uploadEpoch, d.uploadWidth, d.uploadColor = d.epoch, width, color
			d.uploadScreen = d.screen
			var encoded bytes.Buffer
			compressor := zlib.NewWriter(&encoded)
			_, _ = compressor.Write(bytes.Repeat(color[:], width))
			_ = compressor.Close()
			payload := base64.StdEncoding.EncodeToString(encoded.Bytes())
			// Direct upload only: no files, shared memory, or persistent images.
			fmt.Fprintf(c.out, "\x1b_Ga=t,I=%d,f=24,s=%d,v=1,o=z;%s\x1b\\", d.upload, width, payload)
		}
		return
	}
	if slices.Equal(placements, d.placements) {
		return
	}
	var frame strings.Builder
	frame.WriteString(terminalSyncStart + "\x1b7")
	// Erasing text does not erase Kitty images. Remove only our placements,
	// never all graphics at a position/z-index or images owned by applications.
	fmt.Fprintf(&frame, "\x1b_Ga=d,d=i,i=%d,q=2\x1b\\", d.image)
	for i, p := range placements {
		// No c/r scaling: native one-pixel height, at the next header's very
		// top pixel, not the underline baseline or center of a spare cell row.
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b_Ga=p,i=%d,p=%d,w=%d,h=1,C=1,z=-1,q=2\x1b\\", p.row+1, d.image, i+1, p.width)
	}
	frame.WriteString("\x1b8" + terminalSyncEnd)
	_, _ = io.WriteString(c.out, frame.String())
	d.placements = placements
}

func dividerReplyNumber(fields, key string) uint32 {
	var found uint32
	for _, field := range strings.Split(fields, ",") {
		k, value, ok := strings.Cut(field, "=")
		if !ok || k != key {
			continue
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || found != 0 || n == 0 {
			return 0
		}
		found = uint32(n)
	}
	return found
}

func (c *terminalCompositor) dividerReportPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &c.dividerPixels
	return d.enabled && (d.probe != 0 || d.upload != 0)
}

func dividerReportPrefix(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, prefix := range []string{"\x1b_Gi=", "\x1b_GI="} {
		if len(data) < len(prefix) && bytes.HasPrefix([]byte(prefix), data) {
			return true
		}
		if bytes.HasPrefix(data, []byte(prefix)) {
			header, _, _ := bytes.Cut(data[len(prefix):], []byte(";"))
			for _, b := range header {
				if b < '0' || b > '9' {
					return b == ',' // Other response fields follow the numeric ID.
				}
			}
			return true
		}
	}
	return false
}

// Called after paste framing but before shell/application key routing. Only
// tagged replies to our outstanding operations and their ordered probe fence
// are consumed; unrelated application APC replies remain application input.
func (c *terminalCompositor) ConsumeDividerReport(sequence []byte) bool {
	if !bytes.HasPrefix(sequence, []byte("\x1b_G")) && !bytes.HasPrefix(sequence, []byte("\x1b[")) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &c.dividerPixels
	if !d.enabled {
		return false
	}
	text := string(sequence)
	consumed := false
	if strings.HasPrefix(text, "\x1b_G") {
		fields, result, _ := strings.Cut(text[3:], ";")
		ok := strings.TrimSuffix(result, "\x1b\\") == "OK"
		switch {
		case d.probe != 0 && dividerReplyNumber(fields, "i") == d.probe && dividerReplyNumber(fields, "I") == 0:
			d.probeOK, consumed = ok, true
		case d.upload != 0 && dividerReplyNumber(fields, "I") == d.upload:
			image := dividerReplyNumber(fields, "i")
			if ok && image != 0 && d.uploadEpoch == d.epoch && c.dividerEligible() {
				d.image, d.width, d.color = image, d.uploadWidth, d.uploadColor
			}
			// Leaving our alternate screen already deletes its graphics. Never
			// delete a late ID on the application's screen, where it may be reused.
			if image != 0 && d.uploadScreen == d.screen && d.image == 0 {
				fmt.Fprintf(c.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", image)
			}
			d.upload, consumed = 0, true
			if !ok {
				d.probeOK = false
			}
		}
	} else if d.probe != 0 {
		if strings.HasPrefix(text, "\x1b[6;") && strings.HasSuffix(text, "t") {
			parts := strings.Split(text[4:len(text)-1], ";")
			if len(parts) == 2 {
				h, e1 := strconv.ParseUint(parts[0], 10, 16)
				w, e2 := strconv.ParseUint(parts[1], 10, 16)
				if e1 == nil && e2 == nil && d.probeEpoch == d.epoch {
					d.cellWidth, d.cellHeight = int(w), int(h)
				}
			}
			consumed = true
		} else if strings.HasPrefix(text, "\x1b[?") && strings.HasSuffix(text, "c") {
			if d.probeEpoch == d.epoch {
				d.probed = d.epoch + 1
			}
			d.probe, consumed = 0, true
		}
	}
	if consumed && !c.closed && c.viewport != nil {
		c.viewport.dirty = true
		c.renderOutputViewport()
	}
	return consumed
}

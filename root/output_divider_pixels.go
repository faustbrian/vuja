package root

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type pixelBoundary struct{ row, width int }

type pixelDividerImage struct {
	id    uint32
	color [3]byte
}

// Equal-width boundaries share a transient image. Kitty's I allocation
// returns a terminal-owned ID: guessing an i would overwrite another program's
// image. See https://sw.kovidgoyal.net/kitty/graphics-protocol/ (image IDs/layout).
// Graphics never pass through the transcript, shadow terminal or checkpoint.
type pixelDividers struct {
	enabled                   bool
	epoch, probed, probeEpoch uint64
	screen, uploadScreen      uint64
	probe                     uint32
	probeOK                   bool
	itermGeometry             bool
	cellWidth, cellHeight     int
	upload                    uint32
	uploadEpoch               uint64
	uploadWidth               int
	uploadColor               [3]byte
	images                    map[int]pixelDividerImage
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

func (c *terminalCompositor) deleteDividerImages(free bool) {
	d := &c.dividerPixels
	if free {
		for _, image := range d.images {
			fmt.Fprintf(c.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", image.id)
		}
		clear(d.images)
	} else {
		for i, p := range d.placements {
			if image, ok := d.images[p.width]; ok {
				deleteDividerPlacement(c.out, image.id, i+1)
			}
		}
	}
	d.placements = nil
}

func deleteDividerPlacement(out io.Writer, image uint32, placement int) {
	// iTerm 3.7.3's image-wide d=i frees the pixels too; only p-scoped
	// deletion retains the upload for redraw. KittyImageController.swift,
	// executeDeleteImage, https://github.com/gnachman/iTerm2 (also probed live).
	fmt.Fprintf(out, "\x1b_Ga=d,d=i,i=%d,p=%d,q=2\x1b\\", image, placement)
}

func (c *terminalCompositor) invalidatePixelDividers() {
	d := &c.dividerPixels
	if !d.enabled {
		return
	}
	c.deleteDividerImages(true)
	d.epoch++
	d.cellWidth, d.cellHeight = 0, 0
	d.itermGeometry = false
	// At most one outstanding probe and upload, even on unsupported terminals.
	// Their tagged replies remain ours across resize or native handoff.
}

func (c *terminalCompositor) dividerEligible() bool {
	return c.dividerPixels.enabled && !c.closed && c.viewportAlternate &&
		c.outputViewportEnabled() && c.cardLayoutEnabled() && c.chatboxConfig.BlockDividers
}

func dividerGray(value, background string, light bool) [3]byte {
	gray := byte(70)
	if color := ansi.XParseColor(value); color != nil {
		r, g, b, _ := color.RGBA()
		gray = byte((r + g + b) / (3 * 257))
	}
	// Terminal-palette colors are user-defined ANSI indices, not RGB
	// (config.ApplyVisualPolicy). Use the existing light/dark classification
	// for a neutral fallback rather than inventing RGB values for indices.
	base := uint32(0)
	if light {
		base = 255
	}
	if color := ansi.XParseColor(background); color != nil {
		r, g, b, _ := color.RGBA()
		base = (r + g + b) / (3 * 257)
	}
	// Keep opaque neutral pixels so severity tints cannot color the rule,
	// unlike lowering alpha over the next card's background.
	gray = byte((uint32(gray) + base) / 2)
	return [3]byte{gray, gray, gray}
}

func (c *terminalCompositor) renderPixelDividers(rows []cardRow, cards bool) {
	d := &c.dividerPixels
	if !c.dividerEligible() {
		c.deleteDividerImages(true)
		return
	}
	if !cards || c.transientUIVisible != nil && c.transientUIVisible() {
		c.deleteDividerImages(false)
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
			// iTerm 3.7.3 ignores CSI 16 t but supports ReportCellSize, whose
			// points and backing scale give native image pixels. Never query the
			// whole window: it includes padding, chrome and sibling panes, not
			// the safe text grid shared by the supported terminals.
			// https://iterm2.com/documentation-escape-codes.html (Report Cell Size)
			fmt.Fprintf(c.out, "\x1b_Ga=q,i=%d,f=24,s=1,v=1;AAAA\x1b\\\x1b[16t\x1b]1337;ReportCellSize\x1b\\\x1b[c", d.probe)
		}
		return
	}
	if !d.probeOK || d.cellWidth <= 0 || d.cellHeight <= 0 || c.width <= 0 ||
		c.width > terminalWriterMaxPending/3/d.cellWidth {
		// Geometry replies are untrusted. Limit the one-row RGB allocation to
		// the existing terminal writer's byte budget; don't silently crop it.
		c.deleteDividerImages(true)
		return
	}
	color := dividerGray(c.inputBoxTheme.Muted, c.inputBoxTheme.Background, c.inputBoxTheme.LightBackground)
	var placements []pixelBoundary
	widths := make(map[int]bool)
	pixels := 0
	for y, row := range rows {
		if row.boundary && row.width > 0 && y < c.viewportContentRows() {
			width := min(row.width, c.width) * d.cellWidth
			if !widths[width] {
				// All visible-width sources share the former single image's
				// RGB allocation budget. Width diversity must not multiply it.
				if width > terminalWriterMaxPending/3-pixels {
					c.deleteDividerImages(true)
					return
				}
				widths[width], pixels = true, pixels+width
			}
			placements = append(placements, pixelBoundary{y, width})
		}
	}
	if len(placements) == 0 {
		c.deleteDividerImages(true)
		return
	}
	for width, image := range d.images {
		if !widths[width] || image.color != color {
			c.deleteDividerImages(false)
			fmt.Fprintf(c.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", image.id)
			delete(d.images, width)
		}
	}
	for _, p := range placements {
		if _, ok := d.images[p.width]; !ok && d.upload == 0 {
			d.upload = dividerNonce()
			if d.upload == 0 {
				break
			}
			d.uploadEpoch, d.uploadWidth, d.uploadColor = d.epoch, p.width, color
			d.uploadScreen = d.screen
			var encoded bytes.Buffer
			compressor := zlib.NewWriter(&encoded)
			_, _ = compressor.Write(bytes.Repeat(color[:], p.width))
			_ = compressor.Close()
			payload := base64.StdEncoding.EncodeToString(encoded.Bytes())
			// iTerm 3.7.3 prefers uploaded metadata over source-crop dimensions
			// (KittyImageController.swift:173-195). A native source per width
			// avoids stretching the crop, without c/r scaling its pixel height.
			// https://github.com/gnachman/iTerm2/blob/v3.7.3/sources/InlineImages/KittyImageController.swift
			fmt.Fprintf(c.out, "\x1b_Ga=t,I=%d,f=24,s=%d,v=1,o=z;%s\x1b\\", d.upload, p.width, payload)
		}
	}
	ready := placements[:0]
	for _, p := range placements {
		if _, ok := d.images[p.width]; ok {
			ready = append(ready, p)
		}
	}
	if slices.Equal(ready, d.placements) {
		return
	}
	var frame strings.Builder
	frame.WriteString(terminalSyncStart + "\x1b7")
	// Erasing text does not erase Kitty images. Remove only our placements,
	// never all graphics at a position/z-index or images owned by applications.
	for i, p := range d.placements {
		deleteDividerPlacement(&frame, d.images[p.width].id, i+1)
	}
	for i, p := range ready {
		// No c/r scaling: native one-pixel height, at the next header's very
		// top pixel, not the underline baseline or center of a spare cell row.
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b_Ga=p,i=%d,p=%d,w=%d,h=1,C=1,z=-1,q=2\x1b\\", p.row+1, d.images[p.width].id, i+1, p.width)
	}
	frame.WriteString("\x1b8" + terminalSyncEnd)
	_, _ = io.WriteString(c.out, frame.String())
	d.placements = ready
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
	if !bytes.HasPrefix(sequence, []byte("\x1b_G")) && !bytes.HasPrefix(sequence, []byte("\x1b[")) &&
		!bytes.HasPrefix(sequence, []byte("\x1b]1337;ReportCellSize=")) {
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
			adopted := false
			if ok && image != 0 && d.uploadEpoch == d.epoch && c.dividerEligible() {
				if d.images == nil {
					d.images = make(map[int]pixelDividerImage)
				}
				d.images[d.uploadWidth] = pixelDividerImage{image, d.uploadColor}
				adopted = true
			}
			// Leaving our alternate screen already deletes its graphics. Never
			// delete a late ID on the application's screen, where it may be reused.
			if image != 0 && d.uploadScreen == d.screen && !adopted {
				fmt.Fprintf(c.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", image)
			}
			d.upload, consumed = 0, true
			if !ok {
				d.probeOK = false
			}
		}
	} else if d.probe != 0 {
		if value, ok := strings.CutPrefix(text, "\x1b]1337;ReportCellSize="); ok {
			value = strings.TrimSuffix(strings.TrimSuffix(value, "\x1b\\"), "\x07")
			parts := strings.Split(value, ";")
			if len(parts) == 2 || len(parts) == 3 {
				h, e1 := strconv.ParseFloat(parts[0], 64)
				w, e2 := strconv.ParseFloat(parts[1], 64)
				scale, e3 := 1.0, error(nil)
				if len(parts) == 3 {
					scale, e3 = strconv.ParseFloat(parts[2], 64)
				}
				height, width := math.Round(h*scale), math.Round(w*scale)
				if e1 == nil && e2 == nil && e3 == nil && h > 0 && w > 0 && scale > 0 &&
					height > 0 && height <= 65535 && width > 0 && width <= 65535 && d.probeEpoch == d.epoch {
					d.cellWidth, d.cellHeight = int(width), int(height)
					d.itermGeometry = true
				}
			}
			consumed = true
		} else if strings.HasPrefix(text, "\x1b[6;") && strings.HasSuffix(text, "t") {
			parts := strings.Split(text[4:len(text)-1], ";")
			if len(parts) == 2 {
				h, e1 := strconv.ParseUint(parts[0], 10, 16)
				w, e2 := strconv.ParseUint(parts[1], 10, 16)
				if e1 == nil && e2 == nil && !d.itermGeometry && d.probeEpoch == d.epoch {
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

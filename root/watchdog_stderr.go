package root

import "bytes"

const mallocStackLoggingNoise = "MallocStackLogging: can't turn off malloc stack logging because it was not enabled."

type watchdogStderrDisplay struct {
	pending     []byte
	passthrough bool
}

func (d *watchdogStderrDisplay) Consume(chunk []byte, final bool) []byte {
	out := make([]byte, 0, len(chunk)+len(d.pending))
	for len(chunk) > 0 {
		newline := bytes.IndexByte(chunk, '\n')
		segmentEnd := len(chunk)
		if newline >= 0 {
			segmentEnd = newline + 1
		}
		segment := chunk[:segmentEnd]
		chunk = chunk[segmentEnd:]

		if d.passthrough {
			out = append(out, segment...)
			if newline >= 0 {
				d.passthrough = false
			}
			continue
		}

		d.pending = append(d.pending, segment...)
		if newline >= 0 {
			if !isMallocStackLoggingNoise(d.pending) {
				out = append(out, d.pending...)
			}
			d.pending = nil
			continue
		}

		if !isMallocStackLoggingNoisePrefix(d.pending) {
			out = append(out, d.pending...)
			d.pending = nil
			d.passthrough = true
		}
	}

	if final {
		if !isMallocStackLoggingNoise(d.pending) {
			out = append(out, d.pending...)
		}
		d.pending = nil
		d.passthrough = false
	}

	return out
}

func (d *watchdogStderrDisplay) Reset() {
	d.pending = nil
	d.passthrough = false
}

func isMallocStackLoggingNoise(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})

	const prefix = "vuja("
	const separator = ") "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return false
	}

	remainder := line[len(prefix):]
	closing := bytes.Index(remainder, []byte(separator))
	if closing <= 0 {
		return false
	}
	for _, character := range remainder[:closing] {
		if character < '0' || character > '9' {
			return false
		}
	}

	return bytes.Equal(remainder[closing+len(separator):], []byte(mallocStackLoggingNoise))
}

func isMallocStackLoggingNoisePrefix(line []byte) bool {
	const prefix = "vuja("
	if len(line) <= len(prefix) {
		return bytes.HasPrefix([]byte(prefix), line)
	}
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return false
	}

	remainder := line[len(prefix):]
	digitCount := 0
	for digitCount < len(remainder) && remainder[digitCount] >= '0' && remainder[digitCount] <= '9' {
		digitCount++
	}
	if digitCount == 0 || digitCount > 20 {
		return false
	}
	if digitCount == len(remainder) {
		return true
	}

	const separator = ") "
	afterPID := remainder[digitCount:]
	if len(afterPID) <= len(separator) {
		return bytes.HasPrefix([]byte(separator), afterPID)
	}
	if !bytes.HasPrefix(afterPID, []byte(separator)) {
		return false
	}

	diagnostic := afterPID[len(separator):]
	return len(diagnostic) <= len(mallocStackLoggingNoise) && bytes.HasPrefix([]byte(mallocStackLoggingNoise), diagnostic)
}

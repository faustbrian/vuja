package root

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"syscall"
	"testing"
)

func TestManagedWrapperRequiresInteractiveInputAndOutput(t *testing.T) {
	for name, terminals := range map[string]map[int]bool{
		"both terminals": {10: true, 11: true},
		"input socket":   {10: false, 11: true},
		"output socket":  {10: true, 11: false},
	} {
		t.Run(name, func(t *testing.T) {
			got := hasInteractiveTerminal(10, 11, func(fd int) bool { return terminals[fd] })
			want := terminals[10] && terminals[11]
			if got != want {
				t.Fatalf("expected interactive=%t, got %t", want, got)
			}
		})
	}
}

func TestTerminalPTYClosureRecognizesPlatformEndOfStream(t *testing.T) {
	for name, err := range map[string]error{
		"portable EOF":  io.EOF,
		"Linux PTY EIO": syscall.EIO,
	} {
		t.Run(name, func(t *testing.T) {
			if !terminalPTYClosed(err) {
				t.Fatalf("expected %v to terminate the PTY reader", err)
			}
		})
	}

	if terminalPTYClosed(errors.New("temporary read failure")) {
		t.Fatal("expected an unrelated read failure to remain recoverable")
	}
}

func TestPromptTrackingRetainsOnlyABoundedCurrentLine(t *testing.T) {
	tracked := appendPromptTrackingBytes(nil, bytes.Repeat([]byte("x"), promptTrackingByteLimit+128))
	if len(tracked) != promptTrackingByteLimit {
		t.Fatalf("expected prompt tracking to retain at most %d bytes, got %d", promptTrackingByteLimit, len(tracked))
	}

	tracked = appendPromptTrackingBytes(tracked, []byte("stale\ncurrent"))
	if got := string(tracked); got != "current" {
		t.Fatalf("expected only the current prompt line after a newline, got %q", got)
	}
}

func TestShellMessageReaderContinuesAfterAnOversizedMultilineBuffer(t *testing.T) {
	input := append(bytes.Repeat([]byte("pasted\n"), 8), 0)
	input = append(input, []byte("VUJA_CMD_START\x00")...)
	reader := bufio.NewReaderSize(bytes.NewReader(input), 8)

	message, oversized, err := readShellMessage(reader, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !oversized || len(message) != 0 {
		t.Fatalf("expected the oversized message to be discarded, got oversized=%t message=%q", oversized, message)
	}

	message, oversized, err = readShellMessage(reader, 32)
	if err != nil {
		t.Fatal(err)
	}
	if oversized || string(message) != "VUJA_CMD_START" {
		t.Fatalf("expected the next control message to remain readable, got oversized=%t message=%q", oversized, message)
	}
}

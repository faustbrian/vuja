package vt

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestEmulatorCloseConcurrentIO(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func() Terminal
	}{
		{"emulator", func() Terminal { return NewEmulator(12, 12) }},
		{"safe", func() Terminal { return NewSafeEmulator(12, 12) }},
	} {
		t.Run(factory.name, func(t *testing.T) {
			for range 8 {
				e := factory.new()
				t.Cleanup(func() { _ = e.Close() })
				if _, err := e.WriteString("before"); err != nil {
					t.Fatal(err)
				}
				before := e.String()
				start := make(chan struct{})
				var workers sync.WaitGroup
				for range 16 {
					workers.Go(func() {
						<-start
						n, err := e.Read(make([]byte, 1))
						if n != 0 || !errors.Is(err, io.EOF) {
							t.Errorf("closed read = %d, %v; want 0, EOF", n, err)
						}
					})
				}
				for range 4 {
					workers.Go(func() {
						<-start
						if err := e.Close(); err != nil {
							t.Errorf("concurrent close: %v", err)
						}
					})
				}
				workers.Go(func() {
					<-start
					// No parser mutation: exercise only the closed-state check.
					if n, err := e.Write(nil); n != 0 || (err != nil && !errors.Is(err, io.ErrClosedPipe)) {
						t.Errorf("overlapping empty write = %d, %v", n, err)
					}
				})
				close(start)
				done := make(chan struct{})
				go func() { workers.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("close did not unblock readers")
				}
				if n, err := e.Write([]byte("late output")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("write after close = %d, %v; want 0, ErrClosedPipe", n, err)
				}
				if n, err := e.WriteString("late text"); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("text after close = %d, %v; want 0, ErrClosedPipe", n, err)
				}
				if n, err := e.InputPipe().Write([]byte("late input")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("input after close = %d, %v; want 0, ErrClosedPipe", n, err)
				}
				if n, err := e.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
					t.Fatalf("read after close = %d, %v; want 0, EOF", n, err)
				}
				if e.String() != before {
					t.Fatal("closed write changed the screen")
				}
			}
		})
	}
}

func TestEmulatorCloseAfterInputPipeEOF(t *testing.T) {
	e := NewEmulator(12, 12)
	t.Cleanup(func() { _ = e.Close() })
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(e.InputPipe(), "literal reply")
		written <- err
	}()
	payload := make([]byte, len("literal reply"))
	if _, err := io.ReadFull(e, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "literal reply" {
		t.Fatalf("reply changed: %q", payload)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	closer, ok := e.InputPipe().(io.Closer)
	if !ok {
		t.Fatal("input pipe does not support closing")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Read(payload); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("pipe EOF = %d, %v", n, err)
	}
	for range 2 {
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmulatorCloseUnblocksInputWriter(t *testing.T) {
	e := NewEmulator(12, 12)
	t.Cleanup(func() { _ = e.Close() })
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := e.InputPipe().Write([]byte("pending input"))
		done <- err
	}()
	<-started
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("pending input returned %v; want ErrClosedPipe", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not unblock input writer")
	}
}

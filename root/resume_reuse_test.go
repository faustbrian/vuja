package root

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeLinksRemainReusableAfterCompletedUse(t *testing.T) {
	for _, value := range []string{testCodexResumeID, testClaudeResumeCommand, "claude -w \"feature/one\" -r " + testClaudeResumeID} {
		t.Run(value, func(t *testing.T) {
			server, dir := resumeFixtureServer(t)
			link := server.Observe(value)
			target, _ := parseResumeTarget(value)
			for attempt := 0; attempt < 3; attempt++ {
				var input bytes.Buffer
				if err := dispatchResumeFixture(t, server, link, dir, false, "", 0, &input); err != nil {
					t.Fatalf("fresh click %d at reconciled idle prompt: %v", attempt, err)
				}
				if input.String() != target.command+"\r" {
					t.Fatalf("click submitted the wrong command: %q", input.String())
				}
			}
		})
	}
}

func TestResumeClockRollbackDoesNotInvalidateIdleLink(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "vrc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "clock.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &codexResumeActionServer{
		socketPath: path, token: "0123456789abcdef0123456789abcdef",
		actions: make(chan *codexResumeAction, 1), notices: make(chan string, 4), stop: make(chan struct{}),
		// A wire clock that moved backward sees the last submission in its
		// future even though no work remains. No system clock is modified.
		lastSubmission: time.Now().Add(time.Hour),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.AcceptUnix()
		if err == nil {
			server.handle(connection)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, server, server.Observe(testCodexResumeID), dir, false, "", 0, &input); err != nil {
		t.Fatalf("fresh idle click was invalidated by old wall-clock state: %v", err)
	}
	if input.String() != "codex resume "+testCodexResumeID+"\r" {
		t.Fatal("clock reconciliation changed the resume command")
	}
}

func TestResumeQueuedClicksDoNotCrossSubmissionBoundary(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	values := []string{testCodexResumeID, testClaudeResumeCommand}
	var connections []net.Conn
	for _, value := range values {
		parsed, err := url.Parse(server.Observe(value))
		if err != nil {
			t.Fatal(err)
		}
		dialer := net.Dialer{Timeout: time.Second}
		connection, err := dialer.DialContext(t.Context(), "unix", server.socketPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		_ = connection.SetDeadline(time.Now().Add(4 * time.Second))
		target, _ := parseResumeTarget(value)
		request := fmt.Sprintf("%s\t%s\t%d", parsed.Query().Get("token"), target.id, time.Now().Add(codexResumeActionTimeout).UnixNano())
		if target.claude {
			request += "\t" + target.command
		}
		if _, err := fmt.Fprintln(connection, request); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	// Both requests have been sent before the first submission completes.
	var input bytes.Buffer
	action := <-server.Actions()
	submitCodexResumeAction(action, false, resumeFixtureEditor{Writer: &input}, nil)
	for i, connection := range connections {
		reply, err := bufio.NewReader(connection).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && reply != "ok\n" || i == 1 && !strings.Contains(reply, "already pending") {
			t.Fatalf("queued request %d: %q", i, reply)
		}
	}
	select {
	case <-server.Actions():
		t.Fatal("queued duplicate reached the shell consumer")
	default:
	}
	if err := dispatchResumeFixture(t, server, server.Observe(testClaudeResumeCommand), dir, false, "", 0, &input); err != nil {
		t.Fatal("fresh later request rejected:", err)
	}
}

func TestResumeLinksSurviveOtherDisplayedTargets(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	var values, links []string
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("01234567-89ab-7cde-8fab-%012x", i)
		for _, value := range []string{id, "claude --resume " + id} {
			values = append(values, value)
			links = append(links, server.Observe(value))
		}
	}
	for i, link := range links {
		var input bytes.Buffer
		target, _ := parseResumeTarget(values[i])
		if err := dispatchResumeFixture(t, server, link, dir, false, "", 0, &input); err != nil {
			t.Fatalf("displayed target %d was evicted: %v", i, err)
		}
		if input.String() != target.command+"\r" {
			t.Fatal("another displayed target replaced this link")
		}
	}
}

func TestResumeCapabilityBindsOnlyOwnerAndExactTarget(t *testing.T) {
	server, dir := resumeFixtureServer(t)
	for _, value := range []string{testCodexResumeID, testClaudeResumeCommand} {
		target, _ := parseResumeTarget(value)
		link, err := url.Parse(server.Observe(value))
		if err != nil {
			t.Fatal(err)
		}
		// No issuance timestamp or mutable observation is part of this
		// independently computed capability, so passage of time cannot age it.
		mac := hmac.New(sha256.New, []byte(server.token))
		_, _ = mac.Write([]byte(target.key()))
		if link.Query().Get("token") != hex.EncodeToString(mac.Sum(nil)[:16]) {
			t.Fatal("capability depends on more than owner and exact target")
		}
		query := link.Query()
		query.Set("token", server.token)
		link.RawQuery = query.Encode()
		if err := dispatchCodexResumeURL(link.String(), dir); err == nil {
			t.Fatal("legacy raw owner token authorized an unsigned target")
		}
	}
}

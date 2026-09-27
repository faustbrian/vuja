package root

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalInputFilterDropsTerminalReports(t *testing.T) {
	filter := terminalInputFilter{}
	input := []byte("\x1b]11;rgb:0808/0a0a/0d0d\x1b\\git status\x1b[12;40R")
	got := filter.Filter(input)
	if string(got) != "git status" {
		t.Fatalf("expected only user input, got %q", got)
	}
}

func TestTerminalInputFilterHandlesFragmentedOSCResponse(t *testing.T) {
	filter := terminalInputFilter{}
	if got := filter.Filter([]byte("\x1b]11;rgb:0808")); len(got) != 0 {
		t.Fatalf("expected incomplete response to be buffered, got %q", got)
	}
	got := filter.Filter([]byte("/0a0a/0d0d\x07pwd"))
	if string(got) != "pwd" {
		t.Fatalf("expected command after response, got %q", got)
	}
}

func TestTerminalInputFilterPreservesKeyboardEscapeSequences(t *testing.T) {
	filter := terminalInputFilter{}
	input := []byte("\x1b[A\x1b[B\x1b[C\x1b[D")
	if got := filter.Filter(input); !bytes.Equal(got, input) {
		t.Fatalf("expected arrow keys unchanged, got %q", got)
	}
}

func TestInputFramingPreservesPasteAtEveryReadBoundary(t *testing.T) {
	payload := "one\ntwo" + strings.Join(viewportKeys, "") + "\x1b]0;literal\a\x1b[12;40R"
	sequence := bracketedPasteSequence(payload)
	for split := 1; split < len(sequence); split++ {
		var filter terminalInputFilter
		got := append(filter.Filter(sequence[:split]), filter.Filter(sequence[split:])...)
		if !bytes.Equal(got, sequence) {
			t.Fatalf("split %d changed literal paste: %q", split, got)
		}
		active := false
		var pasted []byte
		for len(got) > 0 {
			action := nextBracketedPasteAction(got, active)
			switch action.kind {
			case pasteActionStart:
				active = true
			case pasteActionData:
				pasted = append(pasted, action.data...)
			case pasteActionEnd:
				active = false
			default:
				t.Fatal("paste bytes escaped into normal input")
			}
			got = got[action.consumed:]
		}
		if active || string(pasted) != payload {
			t.Fatalf("split %d left invalid paste state", split)
		}
	}
}

func TestSharedInputFramingPreservesPasteBeforeEveryOwner(t *testing.T) {
	for _, mode := range []string{"pinned", "terminal"} {
		for _, suggestions := range []bool{false, true} {
			for _, shellOwned := range []bool{false, true} {
				var out bytes.Buffer
				c := newTerminalCompositor(&out, "bottom", "framing", 60, 12)
				t.Cleanup(c.Close)
				c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: mode, OutputLines: 100})
				c.WritePTY(terminalMarkerBytes("framing", "prompt-start"))
				c.WritePTY([]byte("› "))
				c.WritePTY(terminalMarkerBytes("framing", "prompt-end"))
				payload := "one\ntwo" + strings.Join(viewportKeys, "") + "\x1b]0;literal\a\x1b[12;40R"
				sequence := append(bracketedPasteSequence(payload), []byte("after")...)
				var framer terminalInputFilter
				var got []byte
				active := false
				var pasted, normal []byte
				for _, b := range sequence {
					chunk := framer.FilterInput([]byte{b}, c, suggestions, shellOwned)
					got = append(got, chunk...)
					for len(chunk) > 0 {
						action := nextBracketedPasteAction(chunk, active)
						switch action.kind {
						case pasteActionStart:
							active = true
						case pasteActionData:
							pasted = append(pasted, action.data...)
						case pasteActionEnd:
							active = false
						case pasteActionNormal:
							normal = append(normal, chunk[0])
						}
						chunk = chunk[action.consumed:]
					}
				}
				if !bytes.Equal(got, sequence) || active || framer.paste || string(pasted) != payload || string(normal) != "after" {
					t.Fatalf("paste changed for mode=%s suggestions=%v shell=%v", mode, suggestions, shellOwned)
				}
				if c.viewport != nil && c.viewport.frozen != nil {
					t.Fatal("pasted keys changed viewport navigation")
				}
			}
		}
	}
}

func TestSharedInputFramingEscapeTimeoutAndReportScope(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "framing", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("framing", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("framing", "prompt-end"))
	for _, suggestions := range []bool{false, true} {
		var f terminalInputFilter
		if len(f.FilterInput([]byte("\x1b"), c, suggestions, true)) != 0 || !f.EscapePending() {
			t.Fatal("lone Escape dispatched before timeout")
		}
		if string(f.FlushEscape(c, suggestions)) != "\x1b" || f.EscapePending() || len(f.FlushEscape(c, suggestions)) != 0 {
			t.Fatal("Escape timeout did not dispatch exactly once")
		}
	}
	for _, shellOwned := range []bool{false, true} {
		report := []byte("\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[12;40R")
		var f terminalInputFilter
		var got []byte
		for _, b := range report {
			got = append(got, f.FilterInput([]byte{b}, c, false, shellOwned)...)
		}
		if shellOwned && len(got) != 0 || !shellOwned && !bytes.Equal(got, report) {
			t.Fatal("terminal reports reached the wrong owner")
		}
	}
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	var f terminalInputFilter
	f.FilterInput([]byte("\x1b"), c, false, true)
	if len(f.FlushEscape(c, false)) != 0 || c.viewport.frozen != nil {
		t.Fatal("Escape timeout did not return the detached viewport to latest")
	}
}

func TestInputReportKeepsOriginalOwnerAcrossCommandTransition(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	for _, report := range [][]byte{[]byte("\x1b]10;rgb:ffff/ffff/ffff\a"), []byte("\x1b[2;3R")} {
		for _, firstShell := range []bool{true, false} {
			for cut := 1; cut < len(report); cut++ {
				var f terminalInputFilter
				got := f.FilterInput(report[:cut], c, false, firstShell)
				got = append(got, f.FilterInput(report[cut:], c, false, !firstShell)...)
				if firstShell && len(got) != 0 || !firstShell && !bytes.Equal(got, report) {
					t.Fatalf("fragmented reply changed owner: firstShell=%v cut=%d got=%q", firstShell, cut, got)
				}
			}
		}
	}
}

func TestInputFramingKeepsPasteOwnerAndAdjacentEventOwner(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	for _, shell := range []bool{false, true} {
		var f terminalInputFilter
		first := f.FilterEvents([]byte("\x1b[200~first\n"), c, false, shell)
		if len(first) != 1 || first[0].shell != shell {
			t.Fatal("paste opener changed owner")
		}
		next := f.FilterEvents([]byte("second\x1b[201~typed"), c, false, !shell)
		if len(next) != 2 || next[0].shell != shell || next[1].shell != !shell || string(next[0].data) != "second\x1b[201~" || string(next[1].data) != "typed" {
			t.Fatalf("paste and adjacent bytes changed owners: %+v", next)
		}
		f.FilterEvents([]byte("\x1b["), c, false, shell)
		next = f.FilterEvents([]byte("Atyped"), c, false, !shell)
		if len(next) != 2 || next[0].shell != shell || next[1].shell != !shell {
			t.Fatal("framed key and adjacent text changed owners")
		}
	}
}

func TestRunningViewportEscapeTimeoutReturnsToLatest(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	var f terminalInputFilter
	f.FilterEvents([]byte("\x1b"), c, false, false)
	if got := f.FlushEscape(c, false); len(got) != 0 || c.viewport.frozen != nil {
		t.Fatalf("running viewport Escape reached PTY instead of latest: %q", got)
	}
}

func TestFragmentedNavigationKeepsSuggestionOwner(t *testing.T) {
	for _, key := range [][]byte{[]byte("\x1b[5~"), []byte("\x1b[F"), []byte("\x1b")} {
		var out bytes.Buffer
		c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
		c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
		c.WritePTY([]byte("› input"))
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
		c.HandleViewportInput([]byte("\x1b[5~"), false)
		var f terminalInputFilter
		f.FilterEvents(key[:1], c, true, true)
		var got []byte
		if len(key) == 1 {
			got = f.FlushEscape(c, false)
		} else {
			got = flattenTerminalInput(f.FilterEvents(key[1:], c, false, true))
		}
		if !bytes.Equal(got, key) || c.viewport.frozen == nil {
			c.Close()
			t.Fatalf("suggestion-owned key changed owners: key=%q got=%q", key, got)
		}
		c.Close()
	}
}

func TestConsumeNextTokenAcceptanceRequiresGhostText(t *testing.T) {
	if consumeNextTokenAcceptance(6, "") {
		t.Fatal("modified right should fall through when there is no ghost text")
	}
	if !consumeNextTokenAcceptance(6, "status ") {
		t.Fatal("modified right should be consumed when it accepts ghost text")
	}
	if consumeNextTokenAcceptance(0, "status ") {
		t.Fatal("unrecognized input should not be consumed")
	}
}

func TestDetectDarkBackgroundUsesColorFGBGWithoutTerminalQuery(t *testing.T) {
	t.Setenv("COLORFGBG", "15;0")
	if !detectDarkBackground() {
		t.Fatal("expected ANSI black background to be dark")
	}

	t.Setenv("COLORFGBG", "0;15")
	if detectDarkBackground() {
		t.Fatal("expected ANSI bright white background to be light")
	}

	t.Setenv("COLORFGBG", "")
	if !detectDarkBackground() {
		t.Fatal("expected dark fallback when the environment has no background hint")
	}
}

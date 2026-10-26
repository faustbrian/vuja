package root

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestZshEditorOwnsLateMouseReports(t *testing.T) {
	for _, keymap := range []string{"bindkey -e", "bindkey -v", "bindkey -N fixture-map emacs; bindkey -A fixture-map main"} {
		t.Run(keymap, func(t *testing.T) {
			f := newResumeShellFixture(t, keymap)
			const report = "\x1b[<51;16;31M\x1b[<0;16;31m"
			if _, err := f.terminal.Write([]byte("keep" + report + "next")); err != nil {
				t.Fatal(err)
			}
			f.snapshot(t, "keepnext")
			f.wait(t, "FIXTURE_PREBUFFER:")
		})
	}
}

func TestZshMouseGuardPreservesLiteralInput(t *testing.T) {
	for _, mode := range []string{"bindkey -e", "bindkey -v"} {
		for _, input := range []struct{ name, keys, want string }{
			{"literal", "café 日本語 51;16;31M <51;16;31M", "café 日本語 51;16;31M <51;16;31M"},
			{"arrows", "ab\x1b[DZ", "aZb"},
			{"rejected", "\x1b[<12;badnext", "12;badnext"},
			{"rejected-paste", "\x1b[<12;\x1b[200~pasted\x1b[201~next", "12;pastednext"},
			{"rejected-control", "old\x1b[<12;\x15new", "new"},
			{"paste", string(bracketedPasteSequence("café 日本語\n\x1b[<51;16;31M\x15")) + "next", "café 日本語\n\x1b[<51;16;31M\x15next"},
		} {
			t.Run(mode+"/"+input.name, func(t *testing.T) {
				f := newResumeShellFixture(t, mode)
				if _, err := f.terminal.Write([]byte(input.keys)); err != nil {
					t.Fatal(err)
				}
				f.snapshot(t, input.want)
			})
		}
	}
}

func TestZshMouseGuardReleasesIncompletePrefix(t *testing.T) {
	f := newResumeShellFixture(t)
	if _, err := f.terminal.Write([]byte("\x1b[<51;16;")); err != nil {
		t.Fatal(err)
	}
	// Exceeds the bounded malformed-input read, not a process-readiness guess.
	time.Sleep(150 * time.Millisecond)
	if _, err := f.terminal.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "51;16;next")
}

func TestZshMouseGuardPreservesBindingsAndQueuedInput(t *testing.T) {
	for _, setup := range []string{
		`bindkey -v; bindkey -M vicmd '^X^T' _fixture_snapshot`,
		`bindkey -N later-map emacs; bindkey -A later-map main`,
	} {
		t.Run(setup, func(t *testing.T) {
			f := newResumeShellFixture(t, setup, `
_fixture_mouse_queue() { zle -U $'\e[<51;16;31Mnext'; }
zle -N _fixture_mouse_queue
bindkey '^X^K' _fixture_mouse_queue`)
			if _, err := f.terminal.Write([]byte("\x18\x0b")); err != nil {
				t.Fatal(err)
			}
			f.snapshot(t, "next")
		})
	}
	f := newResumeShellFixture(t, `
_fixture_explicit_mouse() { LBUFFER+=owned; }
zle -N _fixture_explicit_mouse
bindkey $'\e[<' _fixture_explicit_mouse`)
	if _, err := f.terminal.Write([]byte("\x1b[<")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "owned")
}

func TestZshMouseGuardPreservesViCommandMode(t *testing.T) {
	f := newResumeShellFixture(t, `bindkey -v; bindkey -M vicmd '^X^T' _fixture_snapshot`)
	if _, err := f.terminal.Write([]byte("text\x1b")); err != nil {
		t.Fatal(err)
	}
	// Let the lone Escape switch Vi mode before sending the mouse envelope.
	time.Sleep(450 * time.Millisecond)
	if _, err := f.terminal.Write([]byte("\x1b[<51;16;31M")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "text")
	if _, err := f.terminal.Write([]byte("iZ")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "texZt")
}

func TestZshMouseGuardHandlesEveryFramedHandoffSplit(t *testing.T) {
	f := newResumeShellFixture(t)
	c, _ := blocksFixture(t, "")
	const report = "\x1b[<51;16;31M"
	for split := 0; split <= len(report); split++ {
		var filter terminalInputFilter
		first := filter.FilterInput([]byte(report[:split]), c, false, false)
		last := filter.FilterInput([]byte(report[split:]+"next"), c, false, true)
		if _, err := f.terminal.Write(append(first, last...)); err != nil {
			t.Fatal(err)
		}
		f.snapshot(t, "next")
		if _, err := f.terminal.Write([]byte{21}); err != nil {
			t.Fatal(err)
		}
		f.snapshot(t, "")
	}
}

func TestZshMouseGuardAfterInterruptedCommand(t *testing.T) {
	f := newResumeShellFixture(t, `fixture_interrupt() { print -u3 -N -r -- FIXTURE_RUNNING; sleep 1; }`)
	for range 2 {
		if _, err := f.terminal.Write([]byte("fixture_interrupt\r")); err != nil {
			t.Fatal(err)
		}
		f.wait(t, "FIXTURE_RUNNING")
		if _, err := f.terminal.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}
		f.wait(t, "FIXTURE_EDITOR_READY")
		if _, err := f.terminal.Write([]byte("\x1b[<51;16;31M")); err != nil {
			t.Fatal(err)
		}
		f.snapshot(t, "")
	}
}

func TestZshMouseGuardUsesEditorWithBackgroundJob(t *testing.T) {
	f := newResumeShellFixture(t, `unsetopt monitor
zsh -d -f -c 'read -r -u5 release' &
fixture_bg=$!
_fixture_bg_check() {
  if kill -0 $fixture_bg && [[ $(ps -o pgid= -p $fixture_bg) == $(ps -o pgid= -p $$) ]]; then
    print -u3 -N -r -- FIXTURE_BACKGROUND_SAME_GROUP
  fi
}
zle -N _fixture_bg_check
bindkey '^X^B' _fixture_bg_check`)
	if _, err := f.terminal.Write([]byte("\x18\x02")); err != nil {
		t.Fatal(err)
	}
	f.wait(t, "FIXTURE_BACKGROUND_SAME_GROUP")
	if _, err := f.terminal.Write([]byte("\x1b[<51;16;31Mnext")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "next")
	if _, err := f.release.Write([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
}

func TestZshMouseGuardLeavesNativeConsumerBytesExact(t *testing.T) {
	const vector = "\x1b[<51;16;31M\x1b[<0;16;31m\x1b[51;16;31M\x1b[M \xc2\xa1\x1b[200~paste\x1b[201~next"
	for _, variant := range []string{"foreground", "no-job-control", "background", "exec"} {
		t.Run(variant, func(t *testing.T) {
			options := ""
			if variant == "no-job-control" || variant == "background" {
				options = "unsetopt monitor"
			}
			f := newResumeShellFixture(t, options)
			script := fmt.Sprintf(`stty raw -echo
print -u3 -N -r -- FIXTURE_NATIVE_READY
LC_ALL=C
builtin read -r -k %d payload
stty sane -echo
print -u3 -N -r -- "FIXTURE_NATIVE:$payload"`, len(vector))
			command := "zsh -d -f -c " + shellQuote(script)
			if variant == "background" {
				command = "zsh -d -f -c 'printf \"\\033[?1003h\\033[?1006h\"; read -r -u5 release' & " + command
			}
			if variant == "exec" {
				command = "exec " + command
			}
			if _, err := f.terminal.Write([]byte(command + "\r")); err != nil {
				t.Fatal(err)
			}
			f.wait(t, "FIXTURE_NATIVE_READY")
			c, _ := blocksFixture(t, "")
			var filter terminalInputFilter
			for _, b := range []byte(vector) {
				// Command-start IPC can still describe the previous prompt even
				// though this acknowledged raw consumer already owns the PTY.
				data := filter.FilterInput([]byte{b}, c, false, variant == "no-job-control")
				if _, err := f.terminal.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			f.wait(t, "FIXTURE_NATIVE:"+vector)
			if variant == "background" {
				_, _ = f.release.Write([]byte("done\n"))
			}
			if variant != "exec" {
				f.wait(t, "FIXTURE_EDITOR_READY")
				if _, err := f.terminal.Write([]byte(strings.Repeat("\x1b[<51;16;31M", 2))); err != nil {
					t.Fatal(err)
				}
				f.snapshot(t, "")
			}
		})
	}
}

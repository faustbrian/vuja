package root

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRetainedPanesKeepIndependentOutputThroughResizeAndRestore(t *testing.T) {
	dir := t.TempDir()
	type pane struct {
		id, marker, other string
		compositor        *terminalCompositor
	}
	panes := []pane{
		{id: "pane-a", marker: "marker-a", other: "pane-b"},
		{id: "pane-b", marker: "marker-b", other: "pane-a"},
	}
	for i := range panes {
		panes[i].compositor, _ = recoveryCompositor(t, dir, panes[i].id, panes[i].marker, nil)
	}
	var workers sync.WaitGroup
	for _, current := range panes {
		workers.Go(func() {
			c := current.compositor
			c.WritePTY(terminalMarkerBytes(current.marker, "command-start"))
			for row := range 20 {
				c.WritePTY([]byte(fmt.Sprintf("%s-row-%02d café 日本語 👩‍💻\r\n", current.id, row)))
				c.Resize(20+row%2*60, 14+row%3)
			}
			c.WritePTY(terminalMarkerBytes(current.marker, "command-end:0"))
			c.Close() // Drain this pane's private checkpoint worker.
		})
	}
	workers.Wait()
	if panes[0].compositor.outputRecovery.path == panes[1].compositor.outputRecovery.path {
		t.Fatal("separate panes share a checkpoint identity")
	}
	// Reverse restoration order to prevent constructor order from establishing
	// which checkpoint or retained cell storage belongs to a pane.
	for i := len(panes) - 1; i >= 0; i-- {
		current := panes[i]
		state, err := current.compositor.outputRecovery.read()
		if err != nil {
			t.Fatal(err)
		}
		restored, _ := recoveryCompositor(t, dir, current.id, "restored-"+current.marker, nil)
		restored.Resize(120-i*40, 18-i*3)
		stored := ansi.Strip(strings.Join(state.Rows, "\n"))
		live := ansi.Strip(strings.Join(retainedModelRows(restored.viewport.model), "\n"))
		for name, text := range map[string]string{"checkpoint": stored, "restored": live} {
			if strings.Contains(text, current.other) {
				t.Fatalf("%s for %s contains another pane's output", name, current.id)
			}
			for row := range 20 {
				want := fmt.Sprintf("%s-row-%02d café 日本語 👩‍💻", current.id, row)
				if !strings.Contains(text, want) {
					t.Fatalf("%s for %s lost complete Unicode row %d", name, current.id, row)
				}
			}
		}
		if restored.width != 120-i*40 || restored.height != 18-i*3 {
			t.Fatal("restored pane inherited another pane's geometry")
		}
	}
}

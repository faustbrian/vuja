package sys

import (
	"testing"

	"github.com/faustbrian/vuja/v2/tests/providerfixture"
)

func TestProcessHealthReportsInventoryWithoutSendingSignals(t *testing.T) {
	for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			dir := providerfixture.Environment(t)
			body := "printf '%s\\n' 'PID COMMAND' '42 fixture' '42 fixture' 'malformed'"
			switch mode {
			case "empty":
				body = "exit 0"
			case "failed":
				body = "exit 7"
			}
			if mode != "missing" {
				providerfixture.Write(t, dir, "ps", body)
			}
			ctx, values := providerfixture.Observe(t, mode)
			results := processGenerator(ctx, nil, "", "")
			count := 0
			if mode == "inventory" {
				count = 1
			}
			if len(results) != count || count == 1 && (results[0].Cmd != "42" || results[0].Desc != "fixture") {
				t.Fatalf("PID filtering/deduplication changed: %+v", results)
			}
			providerfixture.Check(t, *values, "processes", providerfixture.Outcome(mode))
		})
	}
}

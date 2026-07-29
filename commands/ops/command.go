package ops

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "command",
		Description: "Run an external command",
		Options: []spec.Option{
			{Name: "-v", Description: "Print the location of the command"},
		},
	})
}

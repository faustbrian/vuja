package fs

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "mv",
		Description: "move (rename) files",
		Generator:   spec.FileGenerator(),
		Options: []spec.Option{
			{Name: "-i", Description: "interactive"},
			{Name: "-f", Description: "force (overwrite)"},
			{Name: "-n", Description: "no clobber (don't overwrite)"},
			{Name: "-v", Description: "verbose"},
		},
	})
}

package view

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "less",
		Description: "view file contents (scrollable)",
		MaxArgs:     1,
		Generator:   spec.FileGenerator(),
	})
}

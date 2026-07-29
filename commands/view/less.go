package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "less",
		Description: "view file contents (scrollable)",
		MaxArgs:     1,
		Generator:   spec.FileGenerator(),
	})
}

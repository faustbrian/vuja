package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "cot",
		Description: "Command-line utility for CotEditor",
		Generator:   spec.FileGenerator(),
	})
}

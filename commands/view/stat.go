package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "stat",
		Description: "display file status",
		Generator:   spec.FileGenerator(),
	})
}

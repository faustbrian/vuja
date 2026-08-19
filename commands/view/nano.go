package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "nano",
		Description: "Nano",
		Generator:   spec.FileGenerator(),
	})
}

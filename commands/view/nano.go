package view

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "nano",
		Description: "Nano",
		Generator:   spec.FileGenerator(),
	})
}

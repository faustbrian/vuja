package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "vi",
		Description: "Print help message for vi and exit",
		Generator:   spec.FileGenerator(),
		Options: []spec.Option{
			{Name: "-h", Description: "Print help message for vi and exit"},
		},
	})
}

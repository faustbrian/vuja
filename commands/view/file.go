package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "file",
		Description: "determine file type",
		Generator:   spec.FileGenerator(),
	})
}

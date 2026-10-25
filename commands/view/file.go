package view

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "file",
		Description: "determine file type",
		Generator:   spec.FileGenerator(),
	})
}

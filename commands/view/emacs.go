package view

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "emacs",
		Description: "An extensible, customizable, free/libre text editor - and more",
		Generator:   spec.FileGenerator(),
	})
}

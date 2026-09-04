package fs

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "exa",
		Description: "A modern replacement for ls",
	})
}

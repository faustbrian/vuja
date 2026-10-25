package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "exa",
		Description: "A modern replacement for ls",
	})
}

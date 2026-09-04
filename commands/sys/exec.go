package sys

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "exec",
		Description: "Replace the current shell with a program",
	})
}

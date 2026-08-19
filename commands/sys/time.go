package sys

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "time",
		Description: "Time how long a command takes!",
	})
}

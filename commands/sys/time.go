package sys

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "time",
		Description: "Time how long a command takes!",
	})
}

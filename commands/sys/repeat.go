package sys

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "repeat",
		Description: "Interpret the result as a number and repeat the commands this many times",
	})
}

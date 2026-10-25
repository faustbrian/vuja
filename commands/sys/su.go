package sys

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "su",
		Description: "(no letter) The same as -l",
		Options: []spec.Option{
			{Name: "-f", Description: "(no letter) The same as -l"},
		},
	})
}

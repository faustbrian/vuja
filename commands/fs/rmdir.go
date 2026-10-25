package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "rmdir",
		Description: "Remove directories",
		Options: []spec.Option{
			{Name: "-p", Description: "Remove each directory of path"},
		},
	})
}

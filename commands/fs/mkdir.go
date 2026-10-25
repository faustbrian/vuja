package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "mkdir",
		Description: "make directories",
		Options: []spec.Option{
			{Name: "-p", Description: "create parent dirs"},
			{Name: "-v", Description: "verbose"},
		},
	})
}

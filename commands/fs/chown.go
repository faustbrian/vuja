package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "chown",
		Description: "change file owner",
		Generator:   spec.FileGenerator(),
		Options: []spec.Option{
			{Name: "-R", Description: "recursive"},
		},
	})
}

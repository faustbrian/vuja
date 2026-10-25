package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "ln",
		Description: "create links",
		Generator:   spec.FileGenerator(),
		Options: []spec.Option{
			{Name: "-s", Description: "symbolic link"},
			{Name: "-f", Description: "force"},
		},
	})
}

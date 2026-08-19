package fs

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "touch",
		Description: "create or update file timestamp",
		Generator:   spec.FileGenerator(),
	})
}

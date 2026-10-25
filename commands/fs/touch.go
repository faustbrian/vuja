package fs

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "touch",
		Description: "create or update file timestamp",
		Generator:   spec.FileGenerator(),
	})
}

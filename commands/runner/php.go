package runner

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "php",
		Description: "Run the PHP interpreter",
	})
}

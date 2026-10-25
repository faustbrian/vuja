package runner

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "php",
		Description: "Run the PHP interpreter",
	})
}

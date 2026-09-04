package js

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "create-video",
		Description: "CLI used to create remotion video project",
	})
}

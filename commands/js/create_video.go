package js

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "create-video",
		Description: "CLI used to create remotion video project",
	})
}

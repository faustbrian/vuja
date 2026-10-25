package js

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "create-vite",
		Description: "Create a new project powered by Vite",
	})
}

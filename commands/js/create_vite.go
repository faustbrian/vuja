package js

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "create-vite",
		Description: "Create a new project powered by Vite",
	})
}

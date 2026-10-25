package sys

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "eleventy",
		Description: "Eleventy is a simpler static site generator",
	})
}

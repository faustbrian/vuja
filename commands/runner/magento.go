package runner

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "magento",
		Description: "Open-source E-commerce",
	})
}

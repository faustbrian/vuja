package runner

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "magento",
		Description: "Open-source E-commerce",
	})
}

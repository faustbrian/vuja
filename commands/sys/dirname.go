package sys

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "dirname",
		Description: "Return directory portion of pathname",
	})
}

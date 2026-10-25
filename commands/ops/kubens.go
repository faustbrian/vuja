package ops

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "kubens",
		Description: "Switch between Kubernetes-namespaces",
		Options: []spec.Option{
			{Name: "--help", Description: "Show help for kubens"},
			{Name: "--current", Description: "Show current namespace"},
		},
	})
}

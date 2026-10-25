package view

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "du",
		Description: "estimate file space usage",
		Generator:   spec.FileGenerator(),
		Options: []spec.Option{
			{Name: "-h", Description: "human readable"},
			{Name: "-s", Description: "summarize"},
			{Name: "-a", Description: "all files"},
		},
	})
}

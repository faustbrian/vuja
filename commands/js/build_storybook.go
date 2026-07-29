package js

import (
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "build-storybook",
		Description: "Storybook build CLI tools",
		Options: []spec.Option{
			{Name: "-o", Description: "Directory where to store built files"},
			{Name: "-w", Description: "Enables watch mode"},
			{Name: "--loglevel", Description: "Controls level of logging during build"},
		},
	})
}

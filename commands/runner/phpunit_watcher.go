package runner

import (
	"github.com/faustbrian/vuja/v2/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "phpunit-watcher",
		Description: "Automatically rerun PHPUnit tests when source code changes",
		Options: []spec.Option{
			{Name: "--filter", Description: "Watch a specific test"},
		},
	})
}

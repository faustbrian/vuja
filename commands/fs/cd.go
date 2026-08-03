package fs

import (
	"context"
	"strings"

	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "cd",
		Description: "change directory",
		MaxArgs:     0,
		Generator: spec.ImmediateGenerator(func(ctx context.Context, tokens []string, prefix string, partial string) []spec.Suggestion {
			fullQuery := strings.Join(tokens[1:], " ")
			return spec.FileGenerator("/")(ctx, tokens, prefix, fullQuery)
		}),
	})
}

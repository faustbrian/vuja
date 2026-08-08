package spec

import (
	"context"
	"reflect"
	"sync"
)

type GeneratorFunc func(ctx context.Context, tokens []string, prefix string, partial string) []Suggestion

// Spec defines a top-level command structure
type Spec struct {
	Name        string
	Aliases     []string
	Description string
	Icon        string
	Subcommands []Subcommand
	Options     []Option
	Generator   GeneratorFunc
	// DynamicCompletion explicitly permits invoking this command's Cobra
	// completion provider. Vuja never executes an unknown command merely because
	// it appears at the start of the current input.
	DynamicCompletion bool
	MaxArgs           int
}

// Subcommand defines nested command logic
type Subcommand struct {
	Name        string
	Aliases     []string
	Description string
	Icon        string
	Subcommands []Subcommand
	Options     []Option
	Generator   GeneratorFunc
	MaxArgs     int
	Priority    int
}

// Option represents a command flag or option
type Option struct {
	Name        string
	Description string
	Priority    int
}

// Suggestion represents an item in the suggestion menu
type Suggestion struct {
	Cmd        string
	Desc       string
	Icon       string
	Source     string // "history", "spec", "filesystem", "ai"
	Live       bool   // true when the candidate was verified against the current filesystem
	Confidence int    // 0-100
	Priority   int    // static author priority
}

var Registry = map[string]*Spec{}

var immediateGenerators sync.Map

// ImmediateGenerator marks a bounded local provider as safe to execute on the
// lookup path. External-process and otherwise unbounded providers remain
// asynchronously cached by default.
func ImmediateGenerator(generator GeneratorFunc) GeneratorFunc {
	if generator != nil {
		immediateGenerators.Store(reflect.ValueOf(generator).Pointer(), struct{}{})
	}
	return generator
}

func isImmediateGenerator(generator GeneratorFunc) bool {
	if generator == nil {
		return false
	}
	_, ok := immediateGenerators.Load(reflect.ValueOf(generator).Pointer())
	return ok
}

// Register adds a new spec to the global Registry
// example: Register(&Spec{Name: "git"})
func Register(s *Spec) {
	s.Generator = cacheGenerator(s.Generator)
	cacheSubcommandGenerators(s.Subcommands)
	Registry[s.Name] = s
}

func cacheSubcommandGenerators(subcommands []Subcommand) {
	for index := range subcommands {
		subcommands[index].Generator = cacheGenerator(subcommands[index].Generator)
		cacheSubcommandGenerators(subcommands[index].Subcommands)
	}
}

// ResetRegistry clears all registered specs - use in tests only
func ResetRegistry() {
	Registry = make(map[string]*Spec)
	resetGeneratorCache()
	resetAliasScanCache()
}

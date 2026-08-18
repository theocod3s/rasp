package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// New builds a Tool from a typed handler. TIn's struct tags produce the JSON
// Schema at construction time and TIn is also the unmarshal target, so what the
// model was told and what the handler receives cannot drift apart. It is one way
// to produce a Tool, not the definition of one — an MCP server's schema arrives
// already written and passes through untouched (design §3.2).
//
// A field's `json` tag names its property and `omitempty` (or `omitzero`) makes
// it optional; `desc` is the description the model reads, and `enum` lists the
// values a string field may take.
//
// It panics rather than returning an error because a tool is a package-level var
// whose schema is fixed at build time: a struct no schema can describe is a
// programming mistake, and failing at startup beats failing on the first call.
func New[TIn any](name, description string, run func(context.Context, TIn) (Result, error)) Tool {
	switch {
	case name == "":
		panic("tool: a tool with no name cannot be called by the model or found in the registry")
	case description == "":
		panic(fmt.Sprintf("tool: %s has no description, and the description is the prompt text the model chooses on", name))
	case run == nil:
		panic(fmt.Sprintf("tool: %s has no handler", name))
	}
	return &reflected[TIn]{
		name:        name,
		description: description,
		schema:      schemaOf(reflect.TypeFor[TIn]()),
		run:         run,
	}
}

type reflected[TIn any] struct {
	name        string
	description string
	schema      map[string]any
	run         func(context.Context, TIn) (Result, error)
}

func (t *reflected[TIn]) Name() string           { return t.name }
func (t *reflected[TIn]) Description() string    { return t.description }
func (t *reflected[TIn]) Schema() map[string]any { return t.schema }

func (t *reflected[TIn]) Run(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in TIn
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			// Bad arguments are the model's problem to fix, so they go back as an
			// error result rather than a Go error, which would end the turn on
			// something the next attempt could get right (design §12).
			return Result{
				IsError: true,
				Content: fmt.Sprintf("The arguments do not fit %s: %v. Call it again with arguments matching its schema.", t.name, err),
			}, nil
		}
	}
	return t.run(ctx, in)
}

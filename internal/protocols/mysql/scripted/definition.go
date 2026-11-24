package scripted

import (
	"slices"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
)

// definition defines the MySQL binding's members in one runtime.
type definition struct {
	scope     *javascript.Scope
	runtime   *sobek.Runtime
	declared  *Declarations
	callbacks *runtimeCallbacks
}

func define(scope *javascript.Scope) (javascript.Members[Declarations, Callbacks], error) {
	return newDefinition(scope).members()
}

func newDefinition(scope *javascript.Scope) *definition {
	return &definition{scope: scope, runtime: scope.Runtime(), declared: &Declarations{}, callbacks: newRuntimeCallbacks(scope)}
}

// members defines egress.mysql and returns it.
func (d *definition) members() (javascript.Members[Declarations, Callbacks], error) {
	mysql := d.runtime.NewObject()
	d.defineMethod(mysql, "egress.mysql."+statementMethod, statementMethod, d.statement(true))
	d.defineMethod(mysql, "egress.mysql."+statementsMethod, statementsMethod, d.statement(false))
	return javascript.Members[Declarations, Callbacks]{
		Egress: Some[sobek.Value](mysql),
		Declared: func() Declarations {
			return Declarations{Replacements: slices.Clone(d.declared.Replacements)}
		},
		Instance: newCallbacks(d.callbacks),
	}, nil
}

// defineMethod binds one object method, panicking if sobek rejects it.
func (d *definition) defineMethod(object *sobek.Object, qualified, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(d.runtime.NewTypeError("define spectre.%s: %v", qualified, err))
	}
}

// statement starts a statement replacement. One candidate statement combines the reference
// statements it replaces; otherwise one reference statement is split.
func (d *definition) statement(combines bool) func(sobek.FunctionCall) sobek.Value {
	method, owner := statementsMethod, "EgressMySQLStatements"
	if combines {
		method, owner = statementMethod, "EgressMySQLStatement"
	}
	return func(call sobek.FunctionCall) sobek.Value {
		statements := d.patterns(call, "egress.mysql."+method, combines)
		object := d.runtime.NewObject()
		d.defineMethod(object, owner+"."+replacesMethod, replacesMethod, func(call sobek.FunctionCall) sobek.Value {
			sources := d.patterns(call, owner+"."+replacesMethod, !combines)
			replacement := d.runtime.NewObject()
			d.defineMethod(replacement, "EgressMySQLReplacement."+usingMethod, usingMethod, func(call sobek.FunctionCall) sobek.Value {
				using, ok := sobek.AssertFunction(call.Argument(0))
				if len(call.Arguments) != 1 || !ok {
					panic(d.runtime.NewTypeError("spectre.EgressMySQLReplacement.%s requires a function", usingMethod))
				}
				d.declared.Replacements = append(d.declared.Replacements, newReplacement(len(d.declared.Replacements), statements, sources, combines))
				d.callbacks.users = append(d.callbacks.users, using)
				return sobek.Undefined()
			})
			return replacement
		})
		return object
	}
}

// patterns reads a call's statement patterns: exactly one if single, otherwise at least one.
func (d *definition) patterns(call sobek.FunctionCall, qualified string, single bool) []string {
	if single && len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires one statement", qualified))
	}
	if len(call.Arguments) == 0 {
		panic(d.runtime.NewTypeError("spectre.%s requires statements", qualified))
	}
	patterns := make([]string, 0, len(call.Arguments))
	for index := range call.Arguments {
		patterns = append(patterns, d.scope.StringArgument(call, index, qualified, "statement"))
	}
	return patterns
}

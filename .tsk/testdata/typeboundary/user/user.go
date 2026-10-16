package user

import (
	"strings"

	. "github.com/alecthomas/types/optional"

	"example.com/m/lower"
	"example.com/m/names"
)

func Into() {
	_ = names.Ref(lower.Name()) // want `names.Ref is converted from the result of example.com/m/lower.Name; make it return names.Ref`
	name, _ := lower.Pair()
	_ = names.Ref(name) // want `names.Ref is converted from the result of example.com/m/lower.Pair; make it return names.Ref`
}

func OutOf(ref names.Ref, kind names.Kind) {
	lower.Lookup(string(ref))    // want `names.Ref is converted to pass to example.com/m/lower.Lookup; make it take names.Ref`
	lower.Count(int(kind))       // want `names.Kind is converted to pass to example.com/m/lower.Count; make it take names.Kind`
	lower.Labels("a", string(ref)) // want `names.Ref is converted to pass to example.com/m/lower.Labels; make it take names.Ref`
	plain := string(ref) // want `names.Ref is converted to pass to example.com/m/lower.Lookup; make it take names.Ref`
	lower.Lookup(plain)
	lower.Maybe(Some(string(ref))) // want `names.Ref is converted to pass to example.com/m/lower.Maybe; make it take names.Ref`
	wrapped := Some(string(ref)) // want `names.Ref is converted to pass to example.com/m/lower.Maybe; make it take names.Ref`
	lower.Maybe(wrapped)
}

func Allowed(ref names.Ref) {
	_ = names.Ref("constant")
	_ = strings.TrimPrefix(string(ref), "a.")
	_ = string(ref) + "." + "field"
	lower.Any(string(ref))
	_ = names.Local(ref)
	_ = names.Ref(strings.ToLower(lower.Name()))
	lower.Lookup(string(ref)) //nolint:typeboundary // kept for the test
}

package scaffold

import (
	"regexp"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// variableName keeps variables apart from the capitalised built-in values.
var variableName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Variable is a string value a template asks for.
type Variable struct {
	// Name is how templates refer to the value, as {{ .name }}.
	Name string `toml:"name"`
	// Help says what the value is for.
	Help string `toml:"help"`
	// RawDefault is the manifest's default, which must be a string.
	RawDefault any `toml:"default"`
	// Default is a template rendered with the built-in values and the earlier
	// variables.
	Default Option[string] `toml:"-"`
	// Choices are the only values allowed.
	Choices []string `toml:"choices"`
}

// normalise checks a variable read from a manifest and fills in its default.
func (v Variable) normalise() (Variable, error) {
	if !variableName.MatchString(v.Name) {
		return Variable{}, errors.Errorf("variable name %q must match %s", v.Name, variableName)
	}
	if v.Help == "" {
		return Variable{}, errors.Errorf("variable %q has no help", v.Name)
	}
	switch raw := v.RawDefault.(type) {
	case nil:
	case string:
		v.Default = Some(raw)
	default:
		return Variable{}, errors.Errorf("variable %q default must be a string", v.Name)
	}
	v.RawDefault = nil
	return v, nil
}

// check reports a value outside the variable's choices.
func (v Variable) check(value string) error {
	if len(v.Choices) > 0 && !slices.Contains(v.Choices, value) {
		return errors.Errorf("%s: %q is not one of %s", v.Name, value, strings.Join(v.Choices, ", "))
	}
	return nil
}

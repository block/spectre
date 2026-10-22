package scaffold

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// VariableType is the kind of value a template variable holds.
type VariableType string

// The variable types. A list is written comma-separated on the command line.
const (
	String VariableType = "string"
	Bool   VariableType = "bool"
	List   VariableType = "list"
)

// variableName keeps variables apart from the capitalised built-in values.
var variableName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Variable is a value a template asks for.
type Variable struct {
	// Name is how templates refer to the value, as {{ .name }}.
	Name string `toml:"name" json:"name"`
	// Help says what the value is for.
	Help string `toml:"help" json:"help"`
	// Type is the kind of value. It defaults to string.
	Type VariableType `toml:"type" json:"type"`
	// RawDefault is the manifest's default, which must be a string.
	RawDefault any `toml:"default" json:"-"`
	// Default is a template rendered with the built-in values and the earlier
	// variables, then read like a command-line value.
	Default Option[string] `toml:"-" json:"default,omitzero"`
	// Choices are the only values allowed, for each item of a list.
	Choices []string `toml:"choices" json:"choices,omitzero"`
	// Pattern is a regular expression each value, or each item of a list, must match.
	Pattern string `toml:"pattern" json:"pattern,omitzero"`
}

// normalise checks a variable read from a manifest and fills in its type and default.
func (v Variable) normalise() (Variable, error) {
	if !variableName.MatchString(v.Name) {
		return Variable{}, errors.Errorf("variable name %q must match %s", v.Name, variableName)
	}
	if v.Help == "" {
		return Variable{}, errors.Errorf("variable %q has no help", v.Name)
	}
	switch v.Type {
	case "":
		v.Type = String
	case String, Bool, List:
	default:
		return Variable{}, errors.Errorf("variable %q has unknown type %q", v.Name, v.Type)
	}
	if _, err := regexp.Compile(v.Pattern); err != nil {
		return Variable{}, errors.Wrapf(err, "variable %q pattern", v.Name)
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

// parse reads a value as written on the command line.
func (v Variable) parse(raw string) (any, error) {
	switch v.Type {
	case String:
		return raw, nil
	case Bool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.Errorf("%s: %q is not a boolean", v.Name, raw)
		}
		return value, nil
	case List:
		items := []string{}
		for item := range strings.SplitSeq(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		return items, nil
	default:
		return nil, errors.Errorf("variable %q has unknown type %q", v.Name, v.Type)
	}
}

// format writes a parsed value as it is written on the command line.
func format(value any) string {
	switch value := value.(type) {
	case bool:
		return strconv.FormatBool(value)
	case []string:
		return strings.Join(value, ",")
	default:
		return fmt.Sprint(value)
	}
}

// check reports a value outside the variable's choices or pattern.
func (v Variable) check(value any) error {
	var items []string
	switch value := value.(type) {
	case string:
		items = []string{value}
	case []string:
		items = value
	default:
		return nil
	}
	pattern := regexp.MustCompile(v.Pattern)
	for _, item := range items {
		if len(v.Choices) > 0 && !slices.Contains(v.Choices, item) {
			return errors.Errorf("%s: %q is not one of %s", v.Name, item, strings.Join(v.Choices, ", "))
		}
		if v.Pattern != "" && !pattern.MatchString(item) {
			return errors.Errorf("%s: %q does not match %s", v.Name, item, v.Pattern)
		}
	}
	return nil
}

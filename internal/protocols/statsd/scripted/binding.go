// Package scripted is the StatsD binding of the spectre script module. Scripts declare
// rules that rename, compare or ignore both sides' metrics. A rename passes the renamed
// series on to the later rules; a comparison or an ignore decides.
package scripted

import (
	_ "embed"
	"regexp"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
)

// Protocol is "statsd".
const Protocol model.Protocol = "statsd"

//go:embed members.d.ts
var membersDeclaration string

// Handle reads the rules that scripts declared through the StatsD binding, and runs
// their callbacks. Only the StatsD server holds it.
type Handle = javascript.Handle[Declarations, Callbacks]

// Declarations are the StatsD rules that scripts declare, in declaration order.
type Declarations struct {
	Rules []Rule
}

// Selector selects metrics by a name pattern and tags.
type Selector struct {
	Pattern string
	Tags    []TagSelector
}

// TagSelector selects by a tag's value, or by a callback when Value is absent.
type TagSelector struct {
	Name     string
	Value    Option[string]
	Callback int
}

// Rule renames, compares or ignores the metrics it selects. A rule with neither
// a rename nor a comparison ignores them.
type Rule struct {
	Selector Selector
	Rename   Option[int]
	Compare  Option[Comparison]
}

// Comparison is how far the candidate may differ from the reference in a window.
type Comparison struct {
	Tolerance   Tolerance
	Percentiles []Percentile
}

// Percentile is a distribution's percentile, from 0 to 100, and its tolerance.
type Percentile struct {
	Rank      float64
	Tolerance Tolerance
}

// Bound limits which way the candidate may not differ.
type Bound string

const (
	// Both fails a candidate that is too high or too low.
	Both Bound = ""
	// Higher fails only a candidate that is too high.
	Higher Bound = "higher"
	// Lower fails only a candidate that is too low.
	Lower Bound = "lower"
)

// Tolerance allows the candidate to differ by max(Absolute, Relative × reference).
type Tolerance struct {
	Relative float64
	Absolute float64
	Only     Bound
}

// Allows reports whether candidate is close enough to reference.
func (t Tolerance) Allows(reference, candidate float64) bool {
	allowed := max(t.Absolute, t.Relative*reference)
	switch t.Only {
	case Higher:
		return candidate <= reference+allowed
	case Lower:
		return candidate >= reference-allowed
	case Both:
	}
	return candidate <= reference+allowed && candidate >= reference-allowed
}

// NewBinding returns the StatsD binding, which the script loader adds to its bindings, and the
// handle through which only the StatsD server reads it.
func NewBinding() (javascript.Binding, Handle) {
	return javascript.NewBinding(Protocol, membersDeclaration, "", nil, define)
}

// PatternExpression is the regular expression of a name pattern: "*" matches within a
// dot-separated segment and "**" across segments.
func PatternExpression(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, errors.New("StatsD pattern is empty")
	}
	var expression strings.Builder
	expression.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch {
		case strings.HasPrefix(pattern[index:], "**"):
			expression.WriteString(".*")
			index++
		case pattern[index] == '*':
			expression.WriteString("[^.]*")
		default:
			expression.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
		}
	}
	expression.WriteString("$")
	compiled, err := regexp.Compile(expression.String())
	return compiled, errors.Wrapf(err, "StatsD pattern %q", pattern)
}

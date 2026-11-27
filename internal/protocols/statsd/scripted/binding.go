// Package scripted is the StatsD binding of the spectre script module. Scripts declare
// rules that map, compare or ignore both sides' metrics. A mapping passes the mapped
// series on to the later rules; a comparison or an ignore decides.
package scripted

import (
	_ "embed"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/statsd/metric"
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

// Rule maps, compares or ignores the metrics it selects. A rule with neither
// a mapping nor a comparison ignores them.
type Rule struct {
	Selector Selector
	Map      Option[int]
	Compare  Option[Comparison]
}

// Comparison is the statistics a rule checks, in order, and how far each may differ.
type Comparison struct {
	Checks []Check
}

// For is the checks of the statistics that series of class have.
func (c Comparison) For(class metric.Class) []Check {
	fitting := []Check{}
	for _, check := range c.Checks {
		if check.Statistic.Fits(class) {
			fitting = append(fitting, check)
		}
	}
	return fitting
}

// Statistics are the statistics the comparison checks.
func (c Comparison) Statistics() []metric.Statistic {
	statistics := make([]metric.Statistic, 0, len(c.Checks))
	for _, check := range c.Checks {
		statistics = append(statistics, check.Statistic)
	}
	return statistics
}

// Check is a statistic, and how far the candidate's may differ from the reference's.
type Check struct {
	Statistic metric.Statistic
	Tolerance Tolerance
}

// Tolerance bounds the candidate above and below the reference by fractions of the reference.
type Tolerance struct {
	Upper float64
	Lower float64
}

// Allows reports whether candidate is close enough to reference.
func (t Tolerance) Allows(reference, candidate float64) bool {
	return candidate <= reference+t.Upper*math.Abs(reference) && candidate >= reference-t.Lower*math.Abs(reference)
}

// String says what the tolerance allows, as in "up to 20% above and 0% below".
func (t Tolerance) String() string {
	switch {
	case t.Upper == 0 && t.Lower == 0:
		return "no difference"
	case t.Upper == t.Lower:
		return "up to " + percent(t.Upper) + " above or below"
	}
	return "up to " + percent(t.Upper) + " above and " + percent(t.Lower) + " below"
}

// percent writes a fraction as a percentage, as in "20%".
func percent(fraction float64) string {
	return strconv.FormatFloat(fraction*100, 'g', 4, 64) + "%"
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

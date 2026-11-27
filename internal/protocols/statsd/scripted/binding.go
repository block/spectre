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
// An absent bound is unlimited, and a difference up to Slack always passes.
type Tolerance struct {
	Above Option[float64]
	Below Option[float64]
	Slack float64
}

// Allows reports whether candidate is close enough to reference.
func (t Tolerance) Allows(reference, candidate float64) bool {
	gap := func(fraction float64) float64 { return max(t.Slack, fraction*math.Abs(reference)) }
	if above, bounded := t.Above.Get(); bounded && candidate > reference+gap(above) {
		return false
	}
	if below, bounded := t.Below.Get(); bounded && candidate < reference-gap(below) {
		return false
	}
	return true
}

// String says what the tolerance allows, as in "up to 20% above".
func (t Tolerance) String() string {
	above, hasAbove := t.Above.Get()
	below, hasBelow := t.Below.Get()
	var allowed string
	switch {
	case !hasAbove && !hasBelow:
		allowed = "any difference"
	case hasAbove && hasBelow && above == 0 && below == 0:
		allowed = "no difference"
	case hasAbove && hasBelow && above == below:
		allowed = "up to " + percent(above) + " above or below"
	case hasAbove && hasBelow:
		allowed = "up to " + percent(above) + " above and " + percent(below) + " below"
	case hasAbove:
		allowed = "up to " + percent(above) + " above"
	default:
		allowed = "up to " + percent(below) + " below"
	}
	if t.Slack > 0 {
		allowed += ", or a difference of up to " + strconv.FormatFloat(t.Slack, 'g', -1, 64)
	}
	return allowed
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

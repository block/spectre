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

// TagSelector selects metrics by one tag.
//
//sumtype:decl
type TagSelector interface{ tagSelector() }

// TagValue selects metrics whose tag equals Value. It never selects a metric without the tag.
type TagValue struct{ Name, Value string }

// Selects reports whether a tag's value, absent when the metric has no such tag, equals Value.
func (t TagValue) Selects(value Option[string]) bool {
	present, ok := value.Get()
	return ok && present == t.Value
}

// TagScript selects the metrics whose tag a script function accepts.
type TagScript struct {
	Name     string
	Callback int
}

func (TagValue) tagSelector()  {}
func (TagScript) tagSelector() {}

// Rule selects metrics and says what happens to them.
type Rule struct {
	Selector Selector
	Action   Action
}

// Action is what a rule does with the metrics it selects.
//
//sumtype:decl
type Action interface{ action() }

// ActionMap maps the metrics with a script function, then passes them on to the later rules.
type ActionMap struct{ Callback int }

// ActionCompare expects both sides to send the series, and checks the comparison's statistics.
type ActionCompare struct{ Comparison Comparison }

// ActionIgnore drops the metrics from both sides.
type ActionIgnore struct{}

func (ActionMap) action()     {}
func (ActionCompare) action() {}
func (ActionIgnore) action()  {}

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

// Tolerance bounds the candidate above and below the reference by fractions of the reference,
// each widened by Slack in the statistic's own units.
type Tolerance struct {
	Upper float64
	Lower float64
	Slack float64
}

// Exceeds names the bound that candidate exceeds, as in "above the 20% upper bound".
func (t Tolerance) Exceeds(reference, candidate float64) Option[string] {
	switch {
	case candidate > reference+t.Upper*math.Abs(reference)+t.Slack:
		return Some("above the " + percent(t.Upper) + " upper bound" + t.slack())
	case candidate < reference-t.Lower*math.Abs(reference)-t.Slack:
		return Some("below the " + percent(t.Lower) + " lower bound" + t.slack())
	}
	return None[string]()
}

// slack describes a non-zero slack, as in " with 1 of slack".
func (t Tolerance) slack() string {
	if t.Slack == 0 {
		return ""
	}
	return " with " + strconv.FormatFloat(t.Slack, 'g', 4, 64) + " of slack"
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

package statsd

import (
	"context"
	"regexp"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/statsd/metric"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
)

// outcome is what the rules decide for a series.
type outcome string

const (
	// compared series are expected from both sides and compared.
	compared outcome = "compared"
	// ignored series are dropped.
	ignored outcome = "ignored"
	// uncovered series reached no rule that compares or ignores them.
	uncovered outcome = "uncovered"
)

// resolution is what the rules make of one side's series: the series it is compared as,
// the factor for its values, and how it is compared.
type resolution struct {
	outcome    outcome
	series     metric.Series
	scale      float64
	comparison scripted.Comparison
}

// rule is a declared rule with its compiled name pattern.
type rule struct {
	declared scripted.Rule
	name     *regexp.Regexp
}

// selects reports whether the rule selects series, running its tag callbacks.
func (r rule) selects(callbacks scripted.Callbacks, series metric.Series) (bool, error) {
	if !r.name.MatchString(series.Name) {
		return false, nil
	}
	for _, tag := range r.declared.Selector.Tags {
		value, present := series.Tags[tag.Name]
		if want, byValue := tag.Value.Get(); byValue {
			if !present || value != want {
				return false, nil
			}
			continue
		}
		argument := None[string]()
		if present {
			argument = Some(value)
		}
		selected, err := callbacks.Selects(tag.Callback, argument)
		if err != nil || !selected {
			return false, errors.WithStack(err)
		}
	}
	return true, nil
}

func newRule(declared scripted.Rule) (rule, error) {
	name, err := scripted.PatternExpression(declared.Selector.Pattern)
	return rule{declared: declared, name: name}, errors.WithStack(err)
}

// rename is the index of the rule's rename callback, if it renames.
func (r rule) rename() Option[int] {
	return r.declared.Rename
}

// compare is the rule's comparison, if it compares.
func (r rule) compare() Option[scripted.Comparison] {
	return r.declared.Compare
}

// resolver applies the scripts' StatsD rules, once for each distinct series of each side.
type resolver struct {
	scripts  *comparison.Scripts
	handle   scripted.Handle
	rules    []rule
	resolved map[string]resolution
}

func newResolver(scripts *comparison.Scripts, handle scripted.Handle) (*resolver, error) {
	declared := handle.Declared(scripts.Program()).Rules
	rules := make([]rule, 0, len(declared))
	for _, declaredRule := range declared {
		compiled, err := newRule(declaredRule)
		if err != nil {
			return nil, err
		}
		rules = append(rules, compiled)
	}
	return &resolver{scripts: scripts, handle: handle, rules: rules, resolved: map[string]resolution{}}, nil
}

// declared reports whether scripts declared any StatsD rule.
func (r *resolver) declared() bool {
	return len(r.rules) > 0
}

// size is how many distinct series the resolver remembers.
func (r *resolver) size() int {
	return len(r.resolved)
}

// resolve returns the resolution of each of side's series, running the rules' callbacks
// in one evaluator for those not seen before. fresh lists the series seen for the first time.
func (r *resolver) resolve(ctx context.Context, side metric.Side, series []metric.Series) (resolutions []resolution, fresh []int, err error) {
	resolutions = make([]resolution, len(series))
	for index, one := range series {
		known, ok := r.resolved[resolutionKey(side, one)]
		if !ok {
			fresh = append(fresh, index)
		}
		resolutions[index] = known
	}
	if len(fresh) == 0 {
		return resolutions, nil, nil
	}
	err = r.scripts.Evaluate(ctx, func(evaluator *javascript.Evaluator) error {
		callbacks := r.handle.Instance(evaluator)
		for _, index := range fresh {
			resolved, err := r.apply(callbacks, side, series[index])
			if err != nil {
				return errors.Wrapf(err, "StatsD rules for %s", series[index])
			}
			r.resolved[resolutionKey(side, series[index])] = resolved
			resolutions[index] = resolved
		}
		return nil
	})
	return resolutions, fresh, errors.WithStack(err)
}

// apply runs the rules in order: a rename passes its series on, and a comparison or an ignore decides.
func (r *resolver) apply(callbacks scripted.Callbacks, side metric.Side, raw metric.Series) (resolution, error) {
	current := resolution{outcome: uncovered, series: raw, scale: 1}
	for _, candidateRule := range r.rules {
		selected, err := candidateRule.selects(callbacks, current.series)
		if err != nil {
			return resolution{}, err
		}
		if !selected {
			continue
		}
		if index, renames := candidateRule.rename().Get(); renames {
			renamed, err := callbacks.Rename(index, side, current.series)
			if err != nil {
				return resolution{}, errors.WithStack(err)
			}
			value, kept := renamed.Get()
			if !kept {
				current.outcome = ignored
				return current, nil
			}
			current.series, current.scale = value.Series, current.scale*value.Scale
			continue
		}
		if comparison, compares := candidateRule.compare().Get(); compares {
			current.outcome, current.comparison = compared, comparison
			return current, nil
		}
		current.outcome = ignored
		return current, nil
	}
	return current, nil
}

// resolutionKey identifies a side's series, with its exact type, since a rename may read it.
func resolutionKey(side metric.Side, series metric.Series) string {
	return string(side) + "|" + string(series.Type) + "|" + series.Key()
}

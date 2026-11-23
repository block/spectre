package storage

import (
	"maps"
	"time"

	"github.com/block/spectre/internal/model"
)

// mergeAfter is how long a run reports nothing before its counts merge into
// Earlier. A run reporting again after that would count its calls twice.
const mergeAfter = 24 * time.Hour

// Tally adds up the calls of a service environment's mirror runs since its
// count started. Each run reports its counts since the run started.
type Tally struct {
	Service     string           `json:"service"`
	Environment string           `json:"environment"`
	Since       time.Time        `json:"since"`
	Start       model.TotalStart `json:"start"`
	// Runs holds the counts of each recently reporting run, by run ID.
	Runs map[string]RunCalls `json:"runs,omitempty"`
	// Earlier holds the counted calls of runs that stopped reporting.
	Earlier     model.Calls `json:"earlier,omitzero"`
	EarlierRuns int         `json:"earlier_runs,omitzero"`
	// Updated is when the tally last changed, to forget abandoned ones.
	Updated time.Time `json:"updated"`
}

// RunCalls is what one mirror run reported. Its counted calls are those
// beyond Baseline, its counts when the tally last restarted.
type RunCalls struct {
	Baseline model.Calls `json:"baseline,omitzero"`
	Latest   model.Calls `json:"latest,omitzero"`
	Seen     time.Time   `json:"seen"`
}

func (r RunCalls) counted() model.Calls {
	return r.Latest.Minus(r.Baseline)
}

// newTally starts counting a service environment's calls at.
func newTally(service, environment string, start model.TotalStart, at time.Time) Tally {
	return Tally{Service: service, Environment: environment, Since: at, Start: start, Updated: at}
}

// RunCalls returns the calls run counted since the tally started.
func (t Tally) RunCalls(run string) model.Calls {
	return t.Runs[run].counted()
}

// Total returns the calls every run counted since the tally started.
func (t Tally) Total() model.CallTotal {
	total := model.CallTotal{
		Service:     t.Service,
		Environment: t.Environment,
		Since:       t.Since,
		Start:       t.Start,
		Calls:       t.Earlier,
		Runs:        t.EarlierRuns,
	}
	for _, run := range t.Runs {
		if counted := run.counted(); !counted.IsZero() {
			total.Calls = total.Calls.Plus(counted)
			total.Runs++
		}
	}
	return total
}

// counted returns the tally with the latest counts of run, merging runs that
// have reported nothing for mergeAfter. It never changes t's maps.
func (t Tally) counted(run string, calls model.Calls, at time.Time) Tally {
	runs := make(map[string]RunCalls, len(t.Runs)+1)
	for id, stored := range t.Runs {
		if id != run && stored.Seen.Before(at.Add(-mergeAfter)) {
			if counted := stored.counted(); !counted.IsZero() {
				t.Earlier = t.Earlier.Plus(counted)
				t.EarlierRuns++
			}
			continue
		}
		runs[id] = stored
	}
	current := runs[run]
	current.Latest, current.Seen = calls, at
	runs[run] = current
	t.Runs = runs
	t.Updated = maxTime(t.Updated, at)
	return t
}

// restarted returns the tally counting again from at, so each run counts only
// what it reports beyond its latest counts. It ignores a start no later than t's.
func (t Tally) restarted(start model.TotalStart, at time.Time) (Tally, bool) {
	if !at.After(t.Since) {
		return t, false
	}
	runs := maps.Clone(t.Runs)
	for id, run := range runs {
		run.Baseline = run.Latest
		runs[id] = run
	}
	return Tally{
		Service:     t.Service,
		Environment: t.Environment,
		Since:       at,
		Start:       start,
		Runs:        runs,
		Updated:     maxTime(t.Updated, at),
	}, true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

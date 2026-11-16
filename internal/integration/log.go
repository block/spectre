package integration

import (
	"fmt"
	"maps"
	"testing"
)

// Log selects the JSON records a spectre process logged, or those of one of
// its subsystems.
type Log struct {
	process *process
	base    Fields
	watch   *watch
}

// newLog selects the records of p with every one of base, waiting with watch.
func newLog(p *process, base Fields, watch *watch) Log {
	return Log{process: p, base: base, watch: watch}
}

// WaitFor waits until the log has a record with every one of fields, and returns
// the first such record.
func (l Log) WaitFor(t *testing.T, fields Fields) Fields {
	t.Helper()
	selected := l.selection(fields)
	var record Fields
	l.watch.await(t, waitTimeout, l.describe(selected), func() bool {
		var found bool
		record, found = l.process.find(selected)
		return found
	})
	return record
}

// WaitForCount waits until the log has at least count records with every one of fields.
func (l Log) WaitForCount(t *testing.T, fields Fields, count int) {
	t.Helper()
	selected := l.selection(fields)
	l.watch.await(t, waitTimeout, fmt.Sprintf("%d of %s", count, l.describe(selected)), func() bool {
		return l.process.count(selected) >= count
	})
}

// Count returns how many records so far have every one of fields.
func (l Log) Count(fields Fields) int {
	return l.process.count(l.selection(fields))
}

func (l Log) selection(fields Fields) Fields {
	selected := maps.Clone(l.base)
	maps.Copy(selected, fields)
	return selected
}

func (l Log) describe(fields Fields) string {
	return l.process.describe(fields)
}

package state

import (
	"slices"
	"strings"

	"github.com/alecthomas/errors"
)

// markMigrated implements Kinds.MarkMigrated.
func markMigrated(data []byte, kinds Kinds, ids []string) (updated []byte, marked []string, err error) {
	current, err := parse(data, kinds)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	statuses := make(map[string]Status, len(current.All))
	for _, node := range current.All {
		statuses[node.ID] = node.Status
	}
	var unknown []string
	for _, id := range ids {
		status, ok := statuses[id]
		switch {
		case !ok:
			unknown = append(unknown, id)
		case status == Pending && !slices.Contains(marked, id):
			marked = append(marked, id)
		}
	}
	if len(unknown) > 0 {
		return nil, nil, errors.Errorf("not a node: %s", strings.Join(unknown, ", "))
	}
	if len(marked) == 0 {
		return data, nil, nil
	}
	for i, node := range current.All {
		if slices.Contains(marked, node.ID) {
			current.All[i].Status = Migrated
		}
	}
	updated, err = encode(current.All)
	if err != nil {
		return nil, nil, errors.Wrap(err, "encode migration state")
	}
	if _, err := parse(updated, kinds); err != nil {
		return nil, nil, withoutLines(err)
	}
	return updated, marked, nil
}

// withoutLines drops the lines of an invalid rewritten state's problems, which
// match no file on disk.
func withoutLines(err error) error {
	invalid, ok := errors.AsType[*InvalidError](err)
	if !ok {
		return errors.WithStack(err)
	}
	problems := make([]Problem, len(invalid.Problems))
	for i, problem := range invalid.Problems {
		problems[i] = Problem{Message: problem.Message}
	}
	return &InvalidError{Problems: problems}
}

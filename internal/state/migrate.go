package state

import (
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml/v2"
)

// MarkMigrated sets the status of each node in ids to migrated and rewrites
// the state, dropping comments. The result must still be a valid state.
func MarkMigrated(data []byte, catalog *Catalog, ids []string) (updated []byte, marked []string, err error) {
	current, err := Parse(data, catalog)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	statuses := make(map[string]Status, len(current.Nodes))
	for _, node := range current.Nodes {
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
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, nil, errors.Wrap(err, "decode migration state")
	}
	nodes, _ := root[nodesKey].(map[string]any)
	for _, id := range marked {
		node, _ := nodes[id].(map[string]any)
		node[statusKey] = string(Migrated)
	}
	updated, err = toml.Marshal(root)
	if err != nil {
		return nil, nil, errors.Wrap(err, "encode migration state")
	}
	if _, err := Parse(updated, catalog); err != nil {
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

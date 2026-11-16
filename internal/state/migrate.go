package state

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/pelletier/go-toml/v2/unstable"
)

const migratedValue = `"` + string(Migrated) + `"`

// MarkMigrated sets the status of each node in ids to migrated, changing only
// those lines of data. The result must still be a valid state.
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
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(slices.Clone(data), '\n')
	}
	locations := statusLocations(data)
	edits := make([]edit, 0, len(marked))
	for _, id := range marked {
		statusEdit, err := migratedEdit(*locations[id], id)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
		edits = append(edits, statusEdit)
	}
	updated = applyEdits(data, edits)
	if _, err := Parse(updated, catalog); err != nil {
		return nil, nil, errors.WithStack(err)
	}
	return updated, marked, nil
}

// edit replaces the bytes from start to end with text.
type edit struct {
	start, end int
	text       string
}

// applyEdits returns a copy of data with each edit applied. Edits must not overlap.
func applyEdits(data []byte, edits []edit) []byte {
	sorted := slices.Clone(edits)
	slices.SortFunc(sorted, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	var out bytes.Buffer
	position := 0
	for _, change := range sorted {
		out.Write(data[position:change.start])
		out.WriteString(change.text)
		position = change.end
	}
	out.Write(data[position:])
	return out.Bytes()
}

// statusLocation is where a node's status is or can be written in a state file.
type statusLocation struct {
	// value is the status value, if the node has one.
	value Option[unstable.Range]
	// afterHeader starts the line after the node's own table header.
	afterHeader Option[int]
	// beforeSubtable starts the line of the first header of a table inside the node.
	beforeSubtable Option[int]
	// inline is set when a key-value, rather than a header, defines the node.
	inline bool
}

// migratedEdit returns the change that marks node id, found at l, migrated.
func migratedEdit(l statusLocation, id string) (edit, error) {
	if value, ok := l.value.Get(); ok {
		start := int(value.Offset)
		return edit{start: start, end: start + int(value.Length), text: migratedValue}, nil
	}
	if offset, ok := l.afterHeader.Get(); ok {
		return edit{start: offset, end: offset, text: statusKey + " = " + migratedValue + "\n"}, nil
	}
	if offset, ok := l.beforeSubtable.Get(); ok && !l.inline {
		header := "[" + keyPath(nodesKey, id) + "]\n" + statusKey + " = " + migratedValue + "\n\n"
		return edit{start: offset, end: offset, text: header}, nil
	}
	return edit{}, errors.Errorf("%s is not written as a [%s] table, so set its status by hand", keyPath(nodesKey, id), keyPath(nodesKey, id))
}

// statusLocations finds, for each node of a decoded state file, where its
// status is or where a status line can go.
func statusLocations(data []byte) map[string]*statusLocation {
	locations := map[string]*statusLocation{}
	location := func(id string) *statusLocation {
		if locations[id] == nil {
			locations[id] = &statusLocation{}
		}
		return locations[id]
	}
	parser := unstable.Parser{}
	parser.Reset(data)
	var table []string
	for parser.NextExpression() {
		expression := parser.Expression()
		if expression.Kind == unstable.KeyValue {
			findStatus(expression, table, location)
			continue
		}
		var end int
		table, end = keyParts(expression.Key(), nil)
		if len(table) < 2 || table[0] != nodesKey {
			continue
		}
		found := location(table[1])
		switch {
		case len(table) == 2 && expression.Kind == unstable.Table:
			found.afterHeader = Some(lineEnd(data, end))
		case len(table) > 2 && !found.beforeSubtable.Ok():
			found.beforeSubtable = Some(lineStart(data, end))
		}
	}
	return locations
}

// findStatus records the status value of a key-value inside a node, and marks a
// node that a key-value defines.
func findStatus(keyValue *unstable.Node, table []string, location func(id string) *statusLocation) {
	path, _ := keyParts(keyValue.Key(), table)
	if len(path) >= 2 && path[0] == nodesKey && len(table) < 2 {
		location(path[1]).inline = true
	}
	if len(path) == 3 && path[0] == nodesKey && path[2] == statusKey {
		location(path[1]).value = Some(keyValue.Value().Raw)
		return
	}
	value := keyValue.Value()
	if value.Kind != unstable.InlineTable {
		return
	}
	for children := value.Children(); children.Next(); {
		findStatus(children.Node(), path, location)
	}
}

// keyParts appends the parts of a dotted key to prefix and returns where the key ends.
func keyParts(key unstable.Iterator, prefix []string) (path []string, end int) {
	path = slices.Clone(prefix)
	for key.Next() {
		part := key.Node()
		path = append(path, string(part.Data))
		end = int(part.Raw.Offset + part.Raw.Length)
	}
	return path, end
}

// lineStart returns the offset of the start of the line holding offset.
func lineStart(data []byte, offset int) int {
	return bytes.LastIndexByte(data[:offset], '\n') + 1
}

// lineEnd returns the offset just after the newline that ends the line holding
// offset, or the end of data.
func lineEnd(data []byte, offset int) int {
	index := bytes.IndexByte(data[offset:], '\n')
	if index < 0 {
		return len(data)
	}
	return offset + index + 1
}

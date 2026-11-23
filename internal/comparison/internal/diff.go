package comparisoninternal

import (
	"slices"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// maxDifferences bounds both result size and work after divergence is established.
const maxDifferences = 100

// differ collects where and how two payloads differ. Differences leave the mirror,
// so paths name only keys that an object's declared type names as fields.
type differ struct {
	differences []difference
	truncated   bool
}

// difference is a model.Difference whose path still holds list indexes.
type difference struct {
	path   reportedPath
	change model.Change
	retype Option[model.Retype]
	count  int
}

func newDiffer() *differ {
	return &differ{}
}

// compare returns where two payloads differ, and whether they differ in more
// places. A root is absent once a normaliser removes it.
func (d *differ) compare(reference, candidate Option[schema.Value]) (differences []model.Difference, truncated bool) {
	d.diffItem(reference, candidate, newDocumentPath(nil))
	return mergeIndexes(d.differences), d.truncated
}

// diff structurally compares two payloads after normalisation.
func (d *differ) diff(reference, candidate schema.Value, path documentPath) {
	if len(d.differences) >= maxDifferences {
		d.truncated = d.truncated || !reference.Equal(candidate)
		return
	}
	if reference.Equal(candidate) {
		return
	}
	switch referenceValue := reference.(type) {
	case schema.ObjectValue:
		candidateValue, ok := candidate.(schema.ObjectValue)
		if !ok {
			d.retyped(path.reported(), reference, candidate)
			return
		}
		for _, key := range sortedKeys(referenceValue.Keys(), candidateValue.Keys()) {
			referenceItem, referenceFound := referenceValue.Member(key)
			candidateItem, candidateFound := candidateValue.Member(key)
			d.diffItem(From(referenceItem, referenceFound), From(candidateItem, candidateFound), member(path, reference.Type(), key))
		}
	case schema.ListValue:
		candidateValue, ok := candidate.(schema.ListValue)
		if !ok {
			d.retyped(path.reported(), reference, candidate)
			return
		}
		for index := range max(referenceValue.Len(), candidateValue.Len()) {
			d.diffItem(elementAt(referenceValue, index), elementAt(candidateValue, index), path.appendIndex(index))
		}
	case schema.NullValue, schema.StringValue, schema.NumberValue, schema.BooleanValue:
		if reference.JSONType() != candidate.JSONType() {
			d.retyped(path.reported(), reference, candidate)
			return
		}
		d.add(path.reported(), model.ChangeChanged, None[model.Retype]())
	}
}

// diffItem compares an object member or list element that either side may lack.
func (d *differ) diffItem(reference, candidate Option[schema.Value], path documentPath) {
	referenceItem, referencePresent := reference.Get()
	candidateItem, candidatePresent := candidate.Get()
	switch {
	case referencePresent && !candidatePresent:
		d.add(path.reported(), model.ChangeMissing, None[model.Retype]())
	case !referencePresent && candidatePresent:
		d.add(path.reported(), model.ChangeAdded, None[model.Retype]())
	case referencePresent && candidatePresent:
		d.diff(referenceItem, candidateItem, path)
	}
}

func (d *differ) retyped(path reportedPath, reference, candidate schema.Value) {
	d.add(path, model.ChangeRetyped, Some(model.Retype{Reference: reference.JSONType(), Candidate: candidate.JSONType()}))
}

// elementAt returns a list's element at index, or None past the end of the list.
func elementAt(list schema.ListValue, index int) Option[schema.Value] {
	return From(list.Element(index))
}

// member returns the path of an object's key. Only a field that the object's type
// declares is named; other keys are payload data, so the path holds a wildcard.
func member(path documentPath, object schema.Type, key string) documentPath {
	if declared, ok := schema.NonNull(object).(*schema.ObjectType); ok {
		if _, found := declared.Field(key); found {
			return path.appendField(key)
		}
	}
	return path.appendAnyKey()
}

// add records a difference, counting it against an equal one already recorded.
func (d *differ) add(path reportedPath, change model.Change, retype Option[model.Retype]) {
	for i, recorded := range d.differences {
		if recorded.change == change && recorded.retype == retype && recorded.path == path {
			d.differences[i].count++
			return
		}
	}
	d.differences = append(d.differences, difference{path: path, change: change, retype: retype, count: 1})
}

// mergeIndexes merges differences that differ only in list indexes into one
// whose indexes are [*], so a change to every element is listed once.
func mergeIndexes(differences []difference) []model.Difference {
	type group struct {
		first  difference
		merged model.Difference
		size   int
	}
	var order []string
	groups := map[string]*group{}
	for _, item := range differences {
		key := string(item.change) + " " + item.path.wildcard
		if retype, ok := item.retype.Get(); ok {
			key += " " + string(retype.Reference) + " " + string(retype.Candidate)
		}
		found, ok := groups[key]
		if !ok {
			found = &group{first: item, merged: model.Difference{Path: item.path.wildcard, Change: item.change, Retype: item.retype}}
			groups[key] = found
			order = append(order, key)
		}
		found.size++
		found.merged.Count += item.count
	}
	merged := make([]model.Difference, 0, len(order))
	for _, key := range order {
		found := groups[key]
		if found.size == 1 {
			first := found.first
			found.merged = model.Difference{Path: first.path.text, Change: first.change, Retype: first.retype, Count: first.count}
		}
		merged = append(merged, found.merged)
	}
	return merged
}

// sortedKeys returns the keys of both objects in order, without repeats.
func sortedKeys(reference, candidate []string) []string {
	keys := slices.Concat(reference, candidate)
	slices.Sort(keys)
	return slices.Compact(keys)
}

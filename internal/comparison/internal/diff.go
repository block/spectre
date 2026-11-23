package comparisoninternal

import (
	"reflect"
	"slices"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// maxDifferences bounds both result size and work after divergence is established.
const maxDifferences = 100

// differ collects where and how two payloads differ. Differences leave the mirror,
// so paths name only the object keys that the schema declares as fields.
type differ struct {
	// loaded and root are absent for untyped payloads, whose keys are all payload data.
	loaded      Option[*schema.Schema]
	root        Option[schema.Value]
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

func newDiffer(loaded Option[*schema.Schema], root Option[schema.Value]) *differ {
	return &differ{loaded: loaded, root: root}
}

// compare returns where two payloads differ, and whether they differ in more
// places. A root is absent once a normaliser removes it.
func (d *differ) compare(reference, candidate Option[model.DynamicValue]) (differences []model.Difference, truncated bool) {
	d.diffItem(reference, candidate, newDocumentPath(nil), d.root)
	return mergeIndexes(d.differences), d.truncated
}

// diff structurally compares two payloads after normalisation. value is the
// declared type at path, or None where the payload departs from the schema.
func (d *differ) diff(reference, candidate model.DynamicValue, path documentPath, value Option[schema.Value]) {
	if len(d.differences) >= maxDifferences {
		d.truncated = d.truncated || !reflect.DeepEqual(reference, candidate)
		return
	}
	if reflect.DeepEqual(reference, candidate) {
		return
	}
	switch referenceValue := reference.(type) {
	case map[string]model.DynamicValue:
		candidateValue, ok := candidate.(map[string]model.DynamicValue)
		if !ok {
			d.add(path.reported(), model.ChangeRetyped, reference, candidate)
			return
		}
		for _, key := range sortedKeys(referenceValue, candidateValue) {
			referenceItem, referenceFound := referenceValue[key]
			candidateItem, candidateFound := candidateValue[key]
			itemPath, itemValue := d.member(path, value, key)
			d.diffItem(From(referenceItem, referenceFound), From(candidateItem, candidateFound), itemPath, itemValue)
		}
	case []model.DynamicValue:
		candidateValue, ok := candidate.([]model.DynamicValue)
		if !ok {
			d.add(path.reported(), model.ChangeRetyped, reference, candidate)
			return
		}
		element := None[schema.Value]()
		if declared, typed := value.Get(); typed && declared.Kind == schema.KindList {
			element = derefValue(declared.Element)
		}
		for index := range max(len(referenceValue), len(candidateValue)) {
			d.diffItem(elementAt(referenceValue, index), elementAt(candidateValue, index), path.appendIndex(index), element)
		}
	default:
		change := model.ChangeChanged
		if jsonType(reference) != jsonType(candidate) {
			change = model.ChangeRetyped
		}
		d.add(path.reported(), change, reference, candidate)
	}
}

// diffItem compares an object member or list element that either side may lack.
func (d *differ) diffItem(reference, candidate Option[model.DynamicValue], path documentPath, value Option[schema.Value]) {
	referenceItem, referencePresent := reference.Get()
	candidateItem, candidatePresent := candidate.Get()
	switch {
	case referencePresent && !candidatePresent:
		d.add(path.reported(), model.ChangeMissing, nil, nil)
	case !referencePresent && candidatePresent:
		d.add(path.reported(), model.ChangeAdded, nil, nil)
	default:
		d.diff(referenceItem, candidateItem, path, value)
	}
}

// elementAt returns a list's element at index, or None past the end of the list.
func elementAt(list []model.DynamicValue, index int) Option[model.DynamicValue] {
	if index < len(list) {
		return Some(list[index])
	}
	return None[model.DynamicValue]()
}

// member returns the path and declared type of an object's key. Only a declared
// field is named; other keys are payload data, so the path holds a wildcard.
func (d *differ) member(path documentPath, value Option[schema.Value], key string) (documentPath, Option[schema.Value]) {
	declared, typed := value.Get()
	loaded, hasSchema := d.loaded.Get()
	switch {
	case typed && hasSchema && declared.Kind == schema.KindObject:
		if object, err := loaded.Type(declared.Type); err == nil {
			if field, found := object.Field(key); found {
				return path.appendField(key), Some(field.Value)
			}
		}
	case typed && declared.Kind == schema.KindMap:
		return path.appendAnyKey(), derefValue(declared.Element)
	}
	return path.appendAnyKey(), None[schema.Value]()
}

// add records a difference, counting it against an equal one already recorded.
// reference and candidate give a retyped difference its types.
func (d *differ) add(path reportedPath, change model.Change, reference, candidate model.DynamicValue) {
	retype := None[model.Retype]()
	if change == model.ChangeRetyped {
		retype = Some(model.Retype{Reference: jsonType(reference), Candidate: jsonType(candidate)})
	}
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

// jsonType returns the type of a decoded JSON value. Decoded JSON holds only
// these Go types and numbers, whose Go type depends on the decoder.
func jsonType(value model.DynamicValue) model.JSONType {
	switch value.(type) {
	case nil:
		return model.JSONNull
	case bool:
		return model.JSONBoolean
	case string:
		return model.JSONString
	case map[string]model.DynamicValue:
		return model.JSONObject
	case []model.DynamicValue:
		return model.JSONList
	}
	return model.JSONNumber
}

// sortedKeys returns the keys of both objects in order, without repeats.
func sortedKeys(reference, candidate map[string]model.DynamicValue) []string {
	keys := make([]string, 0, len(reference)+len(candidate))
	for key := range reference {
		keys = append(keys, key)
	}
	for key := range candidate {
		if _, found := reference[key]; !found {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func derefValue(value Option[*schema.Value]) Option[schema.Value] {
	if pointer, ok := value.Get(); ok && pointer != nil {
		return Some(*pointer)
	}
	return None[schema.Value]()
}

package comparisoninternal

import (
	"reflect"
	"slices"
	"sort"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/schema"
)

// maxDifferences bounds both result size and work after divergence is established.
const maxDifferences = 100

// differ collects the paths at which two payloads differ. Paths leave the mirror,
// so they name only the object keys that the schema declares as fields.
type differ struct {
	// loaded and root are absent for untyped payloads, whose keys are all payload data.
	loaded      Option[*schema.Schema]
	root        Option[schema.Value]
	differences []string
}

func newDiffer(loaded Option[*schema.Schema], root Option[schema.Value]) *differ {
	return &differ{loaded: loaded, root: root, differences: []string{}}
}

// compare returns the paths at which two payloads differ, starting at path.
func (d *differ) compare(reference, candidate any, path documentPath) []string {
	d.diff(reference, candidate, path, d.root)
	return d.differences
}

// diff structurally compares two payloads after normalisation. value is the
// declared type at path, or None where the payload departs from the schema.
func (d *differ) diff(reference, candidate any, path documentPath, value Option[schema.Value]) {
	if len(d.differences) >= maxDifferences || reflect.DeepEqual(reference, candidate) {
		return
	}
	switch referenceValue := reference.(type) {
	case map[string]any:
		candidateValue, ok := candidate.(map[string]any)
		if !ok {
			d.add(path.String())
			return
		}
		keys := map[string]struct{}{}
		for key := range referenceValue {
			keys[key] = struct{}{}
		}
		for key := range candidateValue {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			referenceItem, referencePresent := referenceValue[key]
			candidateItem, candidatePresent := candidateValue[key]
			itemPath, itemValue := d.member(path, value, key)
			if referencePresent != candidatePresent {
				d.add(itemPath.String())
				continue
			}
			d.diff(referenceItem, candidateItem, itemPath, itemValue)
		}
	case []any:
		candidateValue, ok := candidate.([]any)
		if !ok {
			d.add(path.String())
			return
		}
		element := None[schema.Value]()
		if declared, typed := value.Get(); typed && declared.Kind == schema.KindList {
			element = derefValue(declared.Element)
		}
		length := max(len(referenceValue), len(candidateValue))
		for index := range length {
			itemPath := path.appendIndex(index)
			if index >= len(referenceValue) || index >= len(candidateValue) {
				d.add(itemPath.String())
				continue
			}
			d.diff(referenceValue[index], candidateValue[index], itemPath, element)
		}
	default:
		d.add(path.String())
	}
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

func (d *differ) add(path string) {
	if len(d.differences) >= maxDifferences || slices.Contains(d.differences, path) {
		return
	}
	d.differences = append(d.differences, path)
}

func derefValue(value Option[*schema.Value]) Option[schema.Value] {
	if pointer, ok := value.Get(); ok && pointer != nil {
		return Some(*pointer)
	}
	return None[schema.Value]()
}

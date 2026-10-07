package comparisoninternal

import (
	"slices"
	"sort"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// occurrence binds a resolved target to one concrete payload path.
type occurrence struct {
	target normalisationTarget
	path   documentPath
}

func newOccurrence(target normalisationTarget, path documentPath) occurrence {
	return occurrence{target: target, path: path}
}

func (o occurrence) kind() targetKind {
	return o.target.kind()
}

func (o occurrence) location() string {
	return o.path.String()
}

func (o occurrence) value(payload *document) documentValue {
	return payload.Value(o.path)
}

func (o occurrence) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	return o.target.normalise(evaluator, value)
}

func (o occurrence) apply(payload *document, normalised documentValue) error {
	if value, present := normalised.normaliserArgument().Get(); present {
		return errors.Wrap(payload.Set(o.path, value), "replace normalised value")
	}
	return errors.Wrap(payload.Delete(o.path), "remove normalised value")
}

func (o occurrence) logAttributes(side string) []any {
	return []any{
		"kind", o.target.kind(),
		"target", o.target.name(),
		"side", side,
		"payload_path", o.path.String(),
	}
}

func (o occurrence) before(other occurrence) bool {
	// Depth makes traversal leaf-first; priority makes field rules precede broader rules.
	if o.path.depth() != other.path.depth() {
		return o.path.depth() > other.path.depth()
	}
	if o.target.priority() != other.target.priority() {
		return o.target.priority() < other.target.priority()
	}
	if o.path.indexedSiblingBefore(other.path) {
		return true
	}
	if other.path.indexedSiblingBefore(o.path) {
		return false
	}
	return o.target.key() < other.target.key()
}

// collectOccurrences freezes traversal before normalisation starts mutating the document.
// Its ordering is independent of registration order.
func collectOccurrences(
	loaded *schema.Schema,
	root *schema.Type,
	targets []normalisationTarget,
	payload *document,
) []occurrence {
	occurrences := []occurrence{}
	for _, target := range targets {
		occurrences = append(occurrences, target.occurrences(loaded, root, payload)...)
	}
	// A field normaliser replaces any message normaliser at the same location.
	fieldPaths := map[string]struct{}{}
	for _, occurrence := range occurrences {
		if occurrence.kind() == targetField {
			fieldPaths[occurrence.location()] = struct{}{}
		}
	}
	occurrences = slices.DeleteFunc(occurrences, func(occurrence occurrence) bool {
		_, replaced := fieldPaths[occurrence.location()]
		return replaced && occurrence.kind() == targetMessage
	})
	sort.SliceStable(occurrences, func(left, right int) bool {
		return occurrences[left].before(occurrences[right])
	})
	return occurrences
}

// pathWithPresence carries discovery state through multi-step field traversal.
type pathWithPresence struct {
	documentPath documentPath
	present      bool
}

func newPathWithPresence(path documentPath, present bool) pathWithPresence {
	return pathWithPresence{documentPath: path, present: present}
}

func (p pathWithPresence) path() documentPath {
	return p.documentPath
}

func (p pathWithPresence) isPresent() bool {
	return p.present
}

// findMessages also reports absent singular message fields of present parents, so
// normalisers see a missing message as undefined, as they do a missing field.
func findMessages(
	loaded *schema.Schema,
	descriptor *schema.Type,
	target string,
	payload *document,
) []pathWithPresence {
	paths := []pathWithPresence{}
	var walk func(schema.Value, documentPath, bool)
	walk = func(value schema.Value, path documentPath, present bool) {
		switch value.Kind {
		case schema.KindObject:
			if value.Type == target {
				paths = append(paths, newPathWithPresence(path, present))
			}
			if !present {
				return
			}
			current, err := loaded.Type(value.Type)
			if err != nil {
				return // The schema has already resolved all references.
			}
			for _, field := range current.Fields {
				fieldPath := path.appendField(field.Name)
				walk(field.Value, fieldPath, payload.Value(fieldPath).isPresent())
			}
		case schema.KindList:
			element, hasElement := value.Element.Get()
			if !hasElement {
				return // The schema has already checked every element type.
			}
			for index := range arrayLength(payload, path) {
				walk(*element, path.appendIndex(index), true)
			}
		case schema.KindMap:
			element, hasElement := value.Element.Get()
			if !hasElement {
				return // The schema has already checked every element type.
			}
			for _, key := range objectKeys(payload, path) {
				walk(*element, path.appendField(key), true)
			}
		case schema.KindString, schema.KindNumber, schema.KindBoolean, schema.KindEnum:
			return
		}
	}
	root := newDocumentPath(nil)
	walk(schema.Value{Kind: schema.KindObject, Type: descriptor.Name}, root, payload.Value(root).isPresent())
	return paths
}

// descendField yields the final field of every present parent, present or not.
func descendField(
	payload *document,
	paths []pathWithPresence,
	step fieldStep,
	last bool,
) []pathWithPresence {
	descended := []pathWithPresence{}
	for _, base := range paths {
		fieldPath := base.path().appendField(step.jsonName())
		present := payload.Value(fieldPath).isPresent()
		if last {
			descended = append(descended, newPathWithPresence(fieldPath, present))
			continue
		}
		if !present {
			continue
		}
		if step.expandsElements() {
			for index := range arrayLength(payload, fieldPath) {
				descended = append(descended, newPathWithPresence(fieldPath.appendIndex(index), true))
			}
			continue
		}
		descended = append(descended, newPathWithPresence(fieldPath, true))
	}
	return descended
}

func arrayLength(payload *document, path documentPath) int {
	array, _ := payload.Value(path).array()
	return len(array)
}

func objectKeys(payload *document, path documentPath) []string {
	object, _ := payload.Value(path).object()
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

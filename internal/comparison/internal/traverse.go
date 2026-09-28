package comparisoninternal

import (
	"slices"
	"sort"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/comparison/javascript"
)

// occurrence binds a resolved target to one concrete payload path.
// present records discovery before earlier normalisers mutate the document.
type occurrence struct {
	target  normalisationTarget
	path    documentPath
	present bool
}

func newOccurrence(target normalisationTarget, path documentPath, present bool) occurrence {
	return occurrence{target: target, path: path, present: present}
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

func (o occurrence) shouldSkip(value documentValue) bool {
	// A normaliser may remove a value that made a later occurrence reachable.
	return o.present && !value.isPresent()
}

func (o occurrence) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	return o.target.normalise(evaluator, value)
}

func (o occurrence) apply(payload *document, normalised documentValue) error {
	if value, present := normalised.normaliserArgument(); present {
		return errors.Wrap(payload.Set(o.path, value), "replace normalised value")
	}
	return errors.Wrap(payload.Delete(o.path), "remove normalised value")
}

func (o occurrence) logAttributes(side string) []any {
	return []any{
		"kind", o.target.kind(),
		"target", o.target.name(),
		"side", side,
		"response_path", o.path.String(),
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
	method protoreflect.MethodDescriptor,
	targets []normalisationTarget,
	payload *document,
) []occurrence {
	occurrences := []occurrence{}
	for _, target := range targets {
		occurrences = append(occurrences, target.occurrences(method, payload)...)
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
	descriptor protoreflect.MessageDescriptor,
	target protoreflect.FullName,
	payload *document,
) []pathWithPresence {
	paths := []pathWithPresence{}
	var walk func(protoreflect.MessageDescriptor, documentPath, bool)
	walk = func(current protoreflect.MessageDescriptor, path documentPath, present bool) {
		if current.FullName() == target {
			paths = append(paths, newPathWithPresence(path, present))
		}
		if !present {
			return
		}
		fields := current.Fields()
		for index := range fields.Len() {
			field := fields.Get(index)
			if field.IsMap() {
				if field.MapValue().Kind() != protoreflect.MessageKind {
					continue
				}
				fieldPath := path.appendField(field.JSONName())
				for _, key := range objectKeys(payload, fieldPath) {
					walk(field.MapValue().Message(), fieldPath.appendField(key), true)
				}
				continue
			}
			if field.Kind() != protoreflect.MessageKind {
				continue
			}
			fieldPath := path.appendField(field.JSONName())
			if field.IsList() {
				for item := range arrayLength(payload, fieldPath) {
					walk(field.Message(), fieldPath.appendIndex(item), true)
				}
				continue
			}
			walk(field.Message(), fieldPath, payload.Value(fieldPath).isPresent())
		}
	}
	root := newDocumentPath(nil)
	walk(descriptor, root, payload.Value(root).isPresent())
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

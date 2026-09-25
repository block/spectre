package comparisoninternal

import (
	"sort"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/comparison/javascript"
)

// occurrence binds a resolved target to one concrete response path.
// present records discovery before earlier comparisons mutate the documents.
type occurrence struct {
	target  comparisonTarget
	path    documentPath
	present bool
}

func newOccurrence(target comparisonTarget, path documentPath, present bool) occurrence {
	return occurrence{target: target, path: path, present: present}
}

func (o occurrence) values(reference, candidate *document) (documentValue, documentValue) {
	return reference.Value(o.path), candidate.Value(o.path)
}

func (o occurrence) shouldSkip(reference, candidate documentValue) bool {
	// A successful child may remove the only value that made this occurrence reachable.
	return o.present && !reference.isPresent() && !candidate.isPresent()
}

func (o occurrence) compare(
	evaluator *javascript.Evaluator,
	reference, candidate documentValue,
) (matched bool, err error) {
	return o.target.compare(evaluator, reference, candidate)
}

func (o occurrence) delete(reference, candidate *document) error {
	if err := reference.Delete(o.path); err != nil {
		return errors.Wrap(err, "delete matched reference value")
	}
	if err := candidate.Delete(o.path); err != nil {
		return errors.Wrap(err, "delete matched candidate value")
	}
	return nil
}

func (o occurrence) difference() string {
	return o.path.String()
}

func (o occurrence) logAttributes() []any {
	return []any{
		"kind", o.target.kind(),
		"target", o.target.name(),
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

// collectOccurrences freezes traversal before comparison starts mutating documents.
// Its ordering is independent of registration order.
func collectOccurrences(
	method protoreflect.MethodDescriptor,
	targets []comparisonTarget,
	reference, candidate *document,
) []occurrence {
	occurrences := []occurrence{}
	for _, target := range targets {
		occurrences = append(occurrences, target.occurrences(method, reference, candidate)...)
	}
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

// findMessages walks the descriptor and the union of paths present in either response.
func findMessages(
	descriptor protoreflect.MessageDescriptor,
	target protoreflect.FullName,
	reference, candidate *document,
) []pathWithPresence {
	paths := []pathWithPresence{}
	var walk func(protoreflect.MessageDescriptor, documentPath)
	walk = func(current protoreflect.MessageDescriptor, path documentPath) {
		referenceValue := reference.Value(path)
		candidateValue := candidate.Value(path)
		if !referenceValue.isPresent() && !candidateValue.isPresent() {
			return
		}
		if current.FullName() == target {
			paths = append(paths, newPathWithPresence(path, true))
		}
		fields := current.Fields()
		for index := range fields.Len() {
			field := fields.Get(index)
			if field.IsMap() {
				if field.MapValue().Kind() != protoreflect.MessageKind {
					continue
				}
				fieldPath := path.appendField(field.JSONName())
				for _, key := range objectKeys(reference, candidate, fieldPath) {
					walk(field.MapValue().Message(), fieldPath.appendField(key))
				}
				continue
			}
			if field.Kind() != protoreflect.MessageKind {
				continue
			}
			fieldPath := path.appendField(field.JSONName())
			if field.IsList() {
				for item := range arrayLength(reference, candidate, fieldPath) {
					walk(field.Message(), fieldPath.appendIndex(item))
				}
				continue
			}
			walk(field.Message(), fieldPath)
		}
	}
	walk(descriptor, newDocumentPath(nil))
	return paths
}

func descendField(
	reference, candidate *document,
	paths []pathWithPresence,
	step fieldStep,
	last bool,
) []pathWithPresence {
	descended := []pathWithPresence{}
	for _, base := range paths {
		fieldPath := base.path().appendField(step.jsonName())
		referenceValue := reference.Value(fieldPath)
		candidateValue := candidate.Value(fieldPath)
		present := referenceValue.isPresent() || candidateValue.isPresent()
		if last {
			descended = append(descended, newPathWithPresence(fieldPath, present))
			continue
		}
		if step.expandsElements() {
			for index := range arrayLength(reference, candidate, fieldPath) {
				itemPath := fieldPath.appendIndex(index)
				referenceItem := reference.Value(itemPath)
				candidateItem := candidate.Value(itemPath)
				descended = append(descended, newPathWithPresence(
					itemPath,
					referenceItem.isPresent() || candidateItem.isPresent(),
				))
			}
			continue
		}
		descended = append(descended, newPathWithPresence(fieldPath, present))
	}
	return descended
}

func arrayLength(reference, candidate *document, path documentPath) int {
	length := 0
	for _, root := range []*document{reference, candidate} {
		array, ok := root.Value(path).array()
		if !ok {
			continue
		}
		if len(array) > length {
			length = len(array)
		}
	}
	return length
}

func objectKeys(reference, candidate *document, path documentPath) []string {
	keys := map[string]struct{}{}
	for _, root := range []*document{reference, candidate} {
		object, ok := root.Value(path).object()
		if !ok {
			continue
		}
		for key := range object {
			keys[key] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	return ordered
}

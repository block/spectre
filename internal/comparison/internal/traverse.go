package comparisoninternal

import (
	"log/slog"
	"slices"
	"sort"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
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

func (o occurrence) logAttributes(side model.Side) []slog.Attr {
	return []slog.Attr{
		slog.Any("kind", o.target.kind()),
		slog.String("target", o.target.name()),
		slog.String("side", string(side)),
		slog.String("payload_path", o.path.String()),
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
	root *schema.ObjectType,
	targets []normalisationTarget,
	payload *document,
) []occurrence {
	occurrences := []occurrence{}
	for _, target := range targets {
		occurrences = append(occurrences, target.occurrences(root, payload)...)
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
// normalisers see a missing message as undefined. A null message is not reported.
func findMessages(
	root *schema.ObjectType,
	target schema.TypeRef,
	payload *document,
) []pathWithPresence {
	paths := []pathWithPresence{}
	var walk func(schema.Type, documentPath, bool)
	walk = func(declared schema.Type, path documentPath, present bool) {
		switch declared := schema.NonNull(declared).(type) {
		case *schema.ObjectType:
			if present && payload.Value(path).isNull() {
				return
			}
			if declared.Name == target {
				paths = append(paths, newPathWithPresence(path, present))
			}
			if !present {
				return
			}
			for _, field := range declared.Fields {
				fieldPath := path.appendField(field.Name)
				walk(field.Type, fieldPath, payload.Value(fieldPath).isPresent())
			}
		case schema.ListType:
			for index := range listLength(payload, path) {
				walk(declared.Element, path.appendIndex(index), true)
			}
		case schema.MapType:
			for _, key := range objectKeys(payload, path) {
				walk(declared.Element, path.appendField(key), true)
			}
		case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType,
			schema.UnknownType, schema.NullableType:
			return
		}
	}
	start := newDocumentPath(nil)
	walk(root, start, payload.Value(start).isPresent())
	return paths
}

// descendField yields the final field of every present, non-null parent, present or not.
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
		if !present || payload.Value(fieldPath).isNull() {
			continue
		}
		if step.expandsElements() {
			for index := range listLength(payload, fieldPath) {
				descended = append(descended, newPathWithPresence(fieldPath.appendIndex(index), true))
			}
			continue
		}
		descended = append(descended, newPathWithPresence(fieldPath, true))
	}
	return descended
}

func listLength(payload *document, path documentPath) int {
	list, _ := payload.Value(path).list()
	return list.Len()
}

func objectKeys(payload *document, path documentPath) []string {
	object, _ := payload.Value(path).object()
	return object.Keys()
}

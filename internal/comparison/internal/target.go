package comparisoninternal

import (
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

type targetKind string

const (
	targetField   targetKind = "field"
	targetMessage targetKind = "message"
)

// normalisationTarget represents one of the resolved target behaviours.
// Polymorphic traversal and dispatch avoid exposing a tagged union to callers.
type normalisationTarget interface {
	key() string
	kind() targetKind
	name() string
	priority() int
	occurrences(root *schema.ObjectType, payload *document) []occurrence
	normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error)
}

// fieldTarget expands a declared path into one occurrence per concrete field.
type fieldTarget struct {
	declared javascript.FieldTarget
	steps    []fieldStep
}

func (t *fieldTarget) key() string {
	return string(t.kind()) + ":" + string(t.declared.Type()) + "\x00" + t.declared.Path()
}

func (t *fieldTarget) kind() targetKind {
	return targetField
}

func (t *fieldTarget) name() string {
	return t.declared.String()
}

func (t *fieldTarget) priority() int {
	return 0
}

func (t *fieldTarget) occurrences(root *schema.ObjectType, payload *document) []occurrence {
	occurrences := []occurrence{}
	for _, base := range findMessages(root, t.declared.Type(), payload) {
		if !base.isPresent() {
			continue
		}
		paths := []pathWithPresence{base}
		for index, step := range t.steps {
			paths = descendField(payload, paths, step, index == len(t.steps)-1)
		}
		for _, path := range paths {
			occurrences = append(occurrences, newOccurrence(t, path.path()))
		}
	}
	return occurrences
}

func (t *fieldTarget) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	normalised, err := evaluator.NormaliseField(t.declared, value.normaliserArgument())
	if err != nil {
		return newDocumentValue(None[schema.Value]()), errors.Wrap(err, "invoke field normaliser")
	}
	return checkNormalised(t.steps[len(t.steps)-1].declared(), normalised)
}

// messageTarget normalises every nested occurrence of one object type, whose values
// have the declared normalised type.
type messageTarget struct {
	incoming schema.TypeRef
	declared *schema.ObjectType
}

func (t *messageTarget) key() string {
	return string(t.kind()) + ":" + t.name()
}

func (t *messageTarget) kind() targetKind {
	return targetMessage
}

func (t *messageTarget) name() string {
	return string(t.incoming)
}

func (t *messageTarget) priority() int {
	return 1
}

func (t *messageTarget) occurrences(root *schema.ObjectType, payload *document) []occurrence {
	paths := findMessages(root, t.declared.Name, payload)
	occurrences := make([]occurrence, 0, len(paths))
	for _, path := range paths {
		occurrences = append(occurrences, newOccurrence(t, path.path()))
	}
	return occurrences
}

func (t *messageTarget) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	normalised, err := evaluator.NormaliseMessage(t.incoming, value.normaliserArgument())
	if err != nil {
		return newDocumentValue(None[schema.Value]()), errors.Wrap(err, "invoke message normaliser")
	}
	return checkNormalised(t.declared, normalised)
}

// checkNormalised types a normaliser's result as the value it was given, unless the
// normaliser removed it.
func checkNormalised(declared schema.Type, normalised Option[schema.Value]) (documentValue, error) {
	value, present := normalised.Get()
	if !present {
		return newDocumentValue(normalised), nil
	}
	checked, err := schema.As(declared, value)
	if err != nil {
		return newDocumentValue(None[schema.Value]()), errors.Wrapf(err, "normalised value is not %s", declared)
	}
	return newDocumentValue(Some(checked)), nil
}

// fieldStep marks repeated intermediate fields that must expand into element paths.
type fieldStep struct {
	descriptor schema.Field
	elements   bool
}

func newFieldStep(descriptor schema.Field, elements bool) fieldStep {
	return fieldStep{descriptor: descriptor, elements: elements}
}

func (s fieldStep) declared() schema.Type {
	return s.descriptor.Type
}

func (s fieldStep) jsonName() string {
	return s.descriptor.Name
}

func (s fieldStep) expandsElements() bool {
	return s.elements
}

func newFieldTarget(declared javascript.FieldTarget, steps []fieldStep) *fieldTarget {
	return &fieldTarget{declared: declared, steps: steps}
}

func newMessageTarget(incoming schema.TypeRef, declared *schema.ObjectType) *messageTarget {
	return &messageTarget{incoming: incoming, declared: declared}
}

// resolveField resolves a field path relative to its root type.
func resolveField(root *schema.ObjectType, path string) ([]fieldStep, error) {
	parts := strings.Split(path, ".")
	steps := make([]fieldStep, 0, len(parts))
	current := root
	for index, part := range parts {
		elements := strings.HasSuffix(part, "[]")
		fieldName := strings.TrimSuffix(part, "[]")
		field, found := current.Field(fieldName)
		if !found {
			return nil, errors.Errorf("type %q has no field %q", current.Name, fieldName)
		}
		declaredType := schema.NonNull(field.Type)
		list, isList := declaredType.(schema.ListType)
		if elements && !isList {
			return nil, errors.Errorf("field %q is not a list", fieldName)
		}
		last := index == len(parts)-1
		if elements && last {
			return nil, errors.New("field target cannot end with []")
		}
		steps = append(steps, newFieldStep(field, elements))
		if last {
			break
		}
		if isList {
			if !elements {
				return nil, errors.Errorf("list field %q requires [] before a child field", fieldName)
			}
			declaredType = schema.NonNull(list.Element)
		}
		object, isObject := declaredType.(*schema.ObjectType)
		if !isObject {
			return nil, errors.Errorf("field %q does not contain an object", fieldName)
		}
		current = object
	}
	return steps, nil
}

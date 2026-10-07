package comparisoninternal

import (
	"strings"

	"github.com/alecthomas/errors"

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
	occurrences(loaded *schema.Schema, root *schema.Type, payload *document) []occurrence
	normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error)
}

// fieldTarget expands a declared path into one occurrence per concrete field.
type fieldTarget struct {
	declared javascript.FieldTarget
	root     *schema.Type
	steps    []fieldStep
}

func (t *fieldTarget) key() string {
	return string(t.kind()) + ":" + t.declared.Type() + "\x00" + t.declared.Path()
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

func (t *fieldTarget) occurrences(loaded *schema.Schema, root *schema.Type, payload *document) []occurrence {
	occurrences := []occurrence{}
	for _, base := range findMessages(loaded, root, t.root.Name, payload) {
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
	return newDocumentValue(normalised), errors.Wrap(err, "invoke field normaliser")
}

// messageTarget normalises every nested occurrence of one object type.
type messageTarget struct {
	targetName string
	descriptor *schema.Type
}

func (t *messageTarget) key() string {
	return string(t.kind()) + ":" + t.name()
}

func (t *messageTarget) kind() targetKind {
	return targetMessage
}

func (t *messageTarget) name() string {
	return t.targetName
}

func (t *messageTarget) priority() int {
	return 1
}

func (t *messageTarget) occurrences(loaded *schema.Schema, root *schema.Type, payload *document) []occurrence {
	paths := findMessages(loaded, root, t.descriptor.Name, payload)
	occurrences := make([]occurrence, 0, len(paths))
	for _, path := range paths {
		occurrences = append(occurrences, newOccurrence(t, path.path()))
	}
	return occurrences
}

func (t *messageTarget) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	normalised, err := evaluator.NormaliseMessage(t.name(), value.normaliserArgument())
	return newDocumentValue(normalised), errors.Wrap(err, "invoke message normaliser")
}

// fieldStep marks repeated intermediate fields that must expand into element paths.
type fieldStep struct {
	descriptor schema.Field
	elements   bool
}

func newFieldStep(descriptor schema.Field, elements bool) fieldStep {
	return fieldStep{descriptor: descriptor, elements: elements}
}

func (s fieldStep) jsonName() string {
	return s.descriptor.Name
}

func (s fieldStep) expandsElements() bool {
	return s.elements
}

func newFieldTarget(declared javascript.FieldTarget, root *schema.Type, steps []fieldStep) *fieldTarget {
	return &fieldTarget{declared: declared, root: root, steps: steps}
}

func newMessageTarget(name string, descriptor *schema.Type) *messageTarget {
	return &messageTarget{targetName: name, descriptor: descriptor}
}

// resolveField resolves a field path relative to its explicitly named root type.
func resolveField(loaded *schema.Schema, declared javascript.FieldTarget) (*schema.Type, []fieldStep, error) {
	root, err := loaded.Type(declared.Type())
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	parts := strings.Split(declared.Path(), ".")
	steps := make([]fieldStep, 0, len(parts))
	current := root
	for index, part := range parts {
		elements := strings.HasSuffix(part, "[]")
		fieldName := strings.TrimSuffix(part, "[]")
		field, found := current.Field(fieldName)
		if !found {
			return nil, nil, errors.Errorf("type %q has no field %q", current.Name, fieldName)
		}
		if elements && field.Value.Kind != schema.KindList {
			return nil, nil, errors.Errorf("field %q is not a list", fieldName)
		}
		last := index == len(parts)-1
		if elements && last {
			return nil, nil, errors.New("field target cannot end with []")
		}
		steps = append(steps, newFieldStep(field, elements))
		if last {
			break
		}
		value := field.Value
		if value.Kind == schema.KindList {
			if !elements {
				return nil, nil, errors.Errorf("list field %q requires [] before a child field", fieldName)
			}
			element, hasElement := value.Element.Get()
			if !hasElement {
				return nil, nil, errors.Errorf("list field %q has no element type", fieldName)
			}
			value = *element
		}
		if value.Kind != schema.KindObject {
			return nil, nil, errors.Errorf("field %q does not contain an object", fieldName)
		}
		current, err = loaded.Type(value.Type)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
	}
	return root, steps, nil
}

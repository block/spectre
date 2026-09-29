package comparisoninternal

import (
	"strings"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

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
	occurrences(root protoreflect.MessageDescriptor, payload *document) []occurrence
	normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error)
}

// fieldTarget expands a descriptor path into one occurrence per concrete field.
type fieldTarget struct {
	targetName string
	root       protoreflect.MessageDescriptor
	steps      []fieldStep
}

func (t *fieldTarget) key() string {
	return string(t.kind()) + ":" + t.name()
}

func (t *fieldTarget) kind() targetKind {
	return targetField
}

func (t *fieldTarget) name() string {
	return t.targetName
}

func (t *fieldTarget) priority() int {
	return 0
}

func (t *fieldTarget) occurrences(root protoreflect.MessageDescriptor, payload *document) []occurrence {
	occurrences := []occurrence{}
	for _, base := range findMessages(root, t.root.FullName(), payload) {
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
	argument, present := value.normaliserArgument()
	normalised, normalisedPresent, err := evaluator.NormaliseField(t.name(), argument, present)
	return newDocumentValue(normalised, normalisedPresent), errors.Wrap(err, "invoke field normaliser")
}

// messageTarget normalises every nested occurrence of one protobuf message type.
type messageTarget struct {
	targetName string
	descriptor protoreflect.MessageDescriptor
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

func (t *messageTarget) occurrences(root protoreflect.MessageDescriptor, payload *document) []occurrence {
	paths := findMessages(root, t.descriptor.FullName(), payload)
	occurrences := make([]occurrence, 0, len(paths))
	for _, path := range paths {
		occurrences = append(occurrences, newOccurrence(t, path.path()))
	}
	return occurrences
}

func (t *messageTarget) normalise(evaluator *javascript.Evaluator, value documentValue) (documentValue, error) {
	argument, present := value.normaliserArgument()
	normalised, normalisedPresent, err := evaluator.NormaliseMessage(t.name(), argument, present)
	return newDocumentValue(normalised, normalisedPresent), errors.Wrap(err, "invoke message normaliser")
}

// fieldStep marks repeated intermediate fields that must expand into element paths.
type fieldStep struct {
	descriptor protoreflect.FieldDescriptor
	elements   bool
}

func newFieldStep(descriptor protoreflect.FieldDescriptor, elements bool) fieldStep {
	return fieldStep{descriptor: descriptor, elements: elements}
}

func (s fieldStep) jsonName() string {
	return s.descriptor.JSONName()
}

func (s fieldStep) expandsElements() bool {
	return s.elements
}

func newFieldTarget(name string, root protoreflect.MessageDescriptor, steps []fieldStep) *fieldTarget {
	return &fieldTarget{targetName: name, root: root, steps: steps}
}

func newMessageTarget(name string, descriptor protoreflect.MessageDescriptor) *messageTarget {
	return &messageTarget{targetName: name, descriptor: descriptor}
}

func resolveTarget(loaded *schema.Schema, kind targetKind, name string) (normalisationTarget, error) {
	switch kind {
	case targetField:
		root, steps, err := resolveField(loaded, name)
		if err != nil {
			return nil, err
		}
		return newFieldTarget(name, root, steps), nil
	case targetMessage:
		message, err := loaded.Message(protoreflect.FullName(name))
		if err != nil {
			return nil, errors.Wrap(err, "resolve message normaliser target")
		}
		return newMessageTarget(name, message), nil
	default:
		return nil, errors.Errorf("unknown normaliser kind %q", kind)
	}
}

// resolveField finds the longest message-name prefix, then resolves its field path.
func resolveField(loaded *schema.Schema, name string) (protoreflect.MessageDescriptor, []fieldStep, error) {
	parts := strings.Split(name, ".")
	for split := len(parts) - 1; split > 0; split-- {
		root, err := loaded.Message(protoreflect.FullName(strings.Join(parts[:split], ".")))
		if err != nil {
			continue
		}
		steps := make([]fieldStep, 0, len(parts)-split)
		current := root
		for index, part := range parts[split:] {
			elements := strings.HasSuffix(part, "[]")
			fieldName := strings.TrimSuffix(part, "[]")
			field := fieldByName(current, fieldName)
			if field == nil {
				return nil, nil, errors.Errorf("message %q has no field %q", current.FullName(), fieldName)
			}
			if elements && !field.IsList() {
				return nil, nil, errors.Errorf("field %q is not repeated", field.FullName())
			}
			last := index == len(parts[split:])-1
			if elements && last {
				return nil, nil, errors.Errorf("field target cannot end with []")
			}
			steps = append(steps, newFieldStep(field, elements))
			if last {
				break
			}
			if field.Kind() != protoreflect.MessageKind || field.IsMap() {
				return nil, nil, errors.Errorf("field %q does not contain a message", field.FullName())
			}
			if field.IsList() && !elements {
				return nil, nil, errors.Errorf("repeated field %q requires [] before a child field", field.FullName())
			}
			current = field.Message()
		}
		return root, steps, nil
	}
	return nil, nil, errors.Errorf("field target %q has no resolvable message prefix", name)
}

func fieldByName(message protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := message.Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if string(field.Name()) == name || field.JSONName() == name {
			return field
		}
	}
	return nil
}

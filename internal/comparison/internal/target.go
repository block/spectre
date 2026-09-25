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
	targetRPC     targetKind = "rpc"
)

// comparisonTarget represents one of the three resolved target behaviours.
// Polymorphic traversal and dispatch avoid exposing a tagged union to callers.
type comparisonTarget interface {
	key() string
	kind() targetKind
	name() string
	priority() int
	occurrences(
		method protoreflect.MethodDescriptor,
		reference, candidate *document,
	) []occurrence
	compare(
		evaluator *javascript.Evaluator,
		reference, candidate documentValue,
	) (matched bool, err error)
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

func (t *fieldTarget) occurrences(
	method protoreflect.MethodDescriptor,
	reference, candidate *document,
) []occurrence {
	occurrences := []occurrence{}
	for _, base := range findMessages(method.Output(), t.root.FullName(), reference, candidate) {
		paths := []pathWithPresence{base}
		for index, step := range t.steps {
			paths = descendField(reference, candidate, paths, step, index == len(t.steps)-1)
		}
		for _, path := range paths {
			occurrences = append(occurrences, newOccurrence(t, path.path(), path.isPresent()))
		}
	}
	return occurrences
}

func (t *fieldTarget) compare(
	evaluator *javascript.Evaluator,
	reference, candidate documentValue,
) (matched bool, err error) {
	referenceValue, referencePresent := reference.comparatorArgument()
	candidateValue, candidatePresent := candidate.comparatorArgument()
	matched, err = evaluator.CompareField(t.name(), referenceValue, referencePresent, candidateValue, candidatePresent)
	return matched, errors.Wrap(err, "invoke field comparator")
}

// messageTarget compares every nested occurrence of one protobuf message type.
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

func (t *messageTarget) occurrences(
	method protoreflect.MethodDescriptor,
	reference, candidate *document,
) []occurrence {
	paths := findMessages(method.Output(), t.descriptor.FullName(), reference, candidate)
	occurrences := make([]occurrence, 0, len(paths))
	for _, path := range paths {
		occurrences = append(occurrences, newOccurrence(t, path.path(), path.isPresent()))
	}
	return occurrences
}

func (t *messageTarget) compare(
	evaluator *javascript.Evaluator,
	reference, candidate documentValue,
) (matched bool, err error) {
	referenceValue, referencePresent := reference.comparatorArgument()
	candidateValue, candidatePresent := candidate.comparatorArgument()
	matched, err = evaluator.CompareMessage(t.name(), referenceValue, referencePresent, candidateValue, candidatePresent)
	return matched, errors.Wrap(err, "invoke message comparator")
}

// methodTarget contributes one root occurrence only when the request method matches.
type methodTarget struct {
	targetName string
	descriptor protoreflect.MethodDescriptor
}

func (t *methodTarget) key() string {
	return string(t.kind()) + ":" + t.name()
}

func (t *methodTarget) kind() targetKind {
	return targetRPC
}

func (t *methodTarget) name() string {
	return t.targetName
}

func (t *methodTarget) priority() int {
	return 2
}

func (t *methodTarget) occurrences(
	method protoreflect.MethodDescriptor,
	_, _ *document,
) []occurrence {
	if t.descriptor.FullName() != method.FullName() {
		return nil
	}
	return []occurrence{newOccurrence(t, newDocumentPath(nil), true)}
}

func (t *methodTarget) compare(
	evaluator *javascript.Evaluator,
	reference, candidate documentValue,
) (matched bool, err error) {
	referenceValue, referencePresent := reference.comparatorArgument()
	candidateValue, candidatePresent := candidate.comparatorArgument()
	matched, err = evaluator.CompareRPC(t.name(), referenceValue, referencePresent, candidateValue, candidatePresent)
	return matched, errors.Wrap(err, "invoke RPC comparator")
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

func newMethodTarget(name string, descriptor protoreflect.MethodDescriptor) *methodTarget {
	return &methodTarget{targetName: name, descriptor: descriptor}
}

func resolveTarget(loaded *schema.Schema, kind targetKind, name string) (comparisonTarget, error) {
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
			return nil, errors.Wrap(err, "resolve message comparator target")
		}
		return newMessageTarget(name, message), nil
	case targetRPC:
		method, err := loaded.Method(protoreflect.FullName(name))
		if err != nil {
			return nil, errors.Wrap(err, "resolve RPC comparator target")
		}
		if method.IsStreamingClient() || method.IsStreamingServer() {
			return nil, errors.New("streaming RPC comparators are not supported")
		}
		return newMethodTarget(name, method), nil
	default:
		return nil, errors.Errorf("unknown comparator kind %q", kind)
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

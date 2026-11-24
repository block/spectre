package descriptors

import (
	"slices"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/schema"
)

// CheckAgreement requires matching declarations for every object message. Raw
// HTTP declarations may have no protobuf counterpart.
func CheckAgreement(registry *Registry, loaded *schema.Schema) error {
	var agreementErr error
	reachable := schemaFiles(registry.Files())
	registry.Files().RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if !reachable[file.Path()] {
			return true
		}
		agreementErr = checkMessages(registry, file.Messages(), loaded)
		return agreementErr == nil
	})
	if agreementErr != nil {
		return agreementErr
	}
	for _, declared := range loaded.Types() {
		message, err := registry.Message(protoreflect.FullName(declared.Name))
		if err == nil && (message.IsMapEntry() || isUntypedMessage(message.FullName()) || isScalarMessage(message.FullName())) {
			return errors.Errorf("type %q cannot declare a protobuf message without an object JSON shape", declared.Name)
		}
	}
	return nil
}

func checkMessages(registry *Registry, messages protoreflect.MessageDescriptors, loaded *schema.Schema) error {
	for index := range messages.Len() {
		message := messages.Get(index)
		// Map entries are implementation details; well-known scalars are inlined in fields.
		if message.IsMapEntry() || isUntypedMessage(message.FullName()) || isScalarMessage(message.FullName()) {
			continue
		}
		expected, err := messageType(message.FullName(), messageFields(registry, message))
		if err != nil {
			return errors.Wrapf(err, "model message %q", message.FullName())
		}
		declared, err := loaded.Type(schema.TypeRef(message.FullName()))
		if err != nil {
			return errors.Wrapf(err, "protobuf message %q needs a declaration", message.FullName())
		}
		if err := sameFields(expected.Fields, declared); err != nil {
			return errors.Wrapf(err, "type %q disagrees with its protobuf message", declared.Name)
		}
		if err := checkMessages(registry, message.Messages(), loaded); err != nil {
			return err
		}
	}
	return nil
}

func sameFields(expected []schema.Field, declared *schema.ObjectType) error {
	for _, field := range expected {
		actual, ok := declared.Field(field.Name)
		if !ok {
			return errors.Errorf("field %q is missing", field.Name)
		}
		if actual.Optional != field.Optional {
			return errors.Errorf("field %q must be %s", field.Name, optionality(field.Optional))
		}
		if _, nullable := actual.Type.(schema.NullableType); nullable {
			return errors.Errorf("field %q of a protobuf message cannot be null", field.Name)
		}
		if !actual.Type.Equal(field.Type) {
			return errors.Errorf("field %q has type %s, expected %s", field.Name, actual.Type, field.Type)
		}
	}
	for _, field := range declared.Fields {
		if !slices.ContainsFunc(expected, func(candidate schema.Field) bool { return candidate.Name == field.Name }) {
			return errors.Errorf("field %q is not in the protobuf message", field.Name)
		}
	}
	return nil
}

func optionality(optional bool) (description string) {
	if optional {
		return "optional"
	}
	return "required"
}

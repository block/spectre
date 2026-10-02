package descriptors

import (
	"slices"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/schema"
)

// CheckAgreement requires matching declarations for every object message and
// unary method. Raw HTTP declarations may have no protobuf counterpart.
func CheckAgreement(registry *Registry, loaded *schema.Schema) error {
	var agreementErr error
	registry.Files().RangeFiles(func(file protoreflect.FileDescriptor) bool {
		agreementErr = checkMessages(registry, file.Messages(), loaded)
		if agreementErr != nil {
			return false
		}
		for index := range file.Services().Len() {
			service := file.Services().Get(index)
			for index := range service.Methods().Len() {
				method := service.Methods().Get(index)
				if method.IsStreamingClient() || method.IsStreamingServer() {
					continue
				}
				if agreementErr = checkMethod(method, loaded); agreementErr != nil {
					return false
				}
			}
		}
		return true
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
	for _, operation := range loaded.Operations() {
		method, err := registry.Method(protoreflect.FullName(operation.Name))
		if err == nil && (method.IsStreamingClient() || method.IsStreamingServer()) {
			return errors.Errorf("operation %q is a streaming protobuf method", operation.Name)
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
		declared, err := loaded.Type(string(message.FullName()))
		if err != nil {
			return errors.Wrapf(err, "protobuf message %q needs a declaration", message.FullName())
		}
		if err := sameFields(expected, declared); err != nil {
			return errors.Wrapf(err, "type %q disagrees with its protobuf message", declared.Name)
		}
		if err := checkMessages(registry, message.Messages(), loaded); err != nil {
			return err
		}
	}
	return nil
}

func checkMethod(method protoreflect.MethodDescriptor, loaded *schema.Schema) error {
	if err := checkMethodTypes(method); err != nil {
		return err
	}
	operation, err := loaded.Operation(string(method.FullName()))
	if err != nil {
		return errors.Wrapf(err, "protobuf method %q needs a declaration", method.FullName())
	}
	if string(method.Input().FullName()) != operation.Request || string(method.Output().FullName()) != operation.Response {
		return errors.Errorf(
			"operation %q has types %s -> %s, but its protobuf method has %s -> %s",
			operation.Name, operation.Request, operation.Response, method.Input().FullName(), method.Output().FullName(),
		)
	}
	return nil
}

func sameFields(expected, declared *schema.Type) error {
	for _, field := range expected.Fields {
		actual, ok := declared.Field(field.Name)
		if !ok {
			return errors.Errorf("field %q is missing", field.Name)
		}
		if actual.Optional != field.Optional {
			return errors.Errorf("field %q must be %s", field.Name, optionality(field.Optional))
		}
		if !actual.Value.Equal(field.Value) {
			return errors.Errorf("field %q has type %s, expected %s", field.Name, actual.Value, field.Value)
		}
	}
	for _, field := range declared.Fields {
		if !slices.ContainsFunc(expected.Fields, func(candidate schema.Field) bool { return candidate.Name == field.Name }) {
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

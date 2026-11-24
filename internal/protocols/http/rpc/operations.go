package rpc

import (
	"maps"
	"slices"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/schema"
)

// Operations indexes the RPC operations declared by a schema, by name. It is
// immutable and safe for concurrent use.
type Operations struct {
	byName map[string]Operation
}

// New reads each schema method as an operation named Service.Method.
func New(methods []schema.Method) *Operations {
	operations := &Operations{byName: make(map[string]Operation, len(methods))}
	for _, method := range methods {
		name := string(method.Interface) + "." + method.Name
		operations.byName[name] = Operation{Name: name, Request: method.Parameter, Response: method.Result}
	}
	return operations
}

// Operation returns the named RPC operation.
func (o *Operations) Operation(name string) (Operation, error) {
	operation, ok := o.byName[name]
	if !ok {
		return Operation{}, errors.Errorf("operation %q is not declared", name)
	}
	return operation, nil
}

// Check requires every unary protobuf method to have an operation of the same types,
// and no operation to be a streaming method. Raw HTTP operations are allowed.
func (o *Operations) Check(registry *descriptors.Registry) error {
	for _, method := range registry.Methods() {
		if method.IsStreamingClient() || method.IsStreamingServer() {
			continue
		}
		if err := o.checkMethod(method); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(o.byName)) {
		method, err := registry.Method(protoreflect.FullName(name))
		if err == nil && (method.IsStreamingClient() || method.IsStreamingServer()) {
			return errors.Errorf("operation %q is a streaming protobuf method", name)
		}
	}
	return nil
}

func (o *Operations) checkMethod(method protoreflect.MethodDescriptor) error {
	if err := descriptors.CheckMethodTypes(method); err != nil {
		return errors.WithStack(err)
	}
	operation, err := o.Operation(string(method.FullName()))
	if err != nil {
		return errors.Wrapf(err, "protobuf method %q needs a declaration", method.FullName())
	}
	if schema.TypeRef(method.Input().FullName()) != operation.Request || schema.TypeRef(method.Output().FullName()) != operation.Response {
		return errors.Errorf(
			"operation %q has types %s -> %s, but its protobuf method has %s -> %s",
			operation.Name, operation.Request, operation.Response, method.Input().FullName(), method.Output().FullName(),
		)
	}
	return nil
}

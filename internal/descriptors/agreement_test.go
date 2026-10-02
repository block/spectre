package descriptors_test

import (
	"slices"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/schema"
)

func TestAgreementRequiresEveryMessage(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		mutate func(set *descriptorpb.FileDescriptorSet)
		missing string
	}{
		"TopLevel": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				file := set.File[len(set.GetFile())-1]
				file.MessageType = append(file.GetMessageType(), &descriptorpb.DescriptorProto{Name: new("Unused")})
			},
			missing: "spectre.sample.v1.Unused",
		},
		"Nested": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				user := set.File[1].MessageType[0]
				user.NestedType = append(user.GetNestedType(), &descriptorpb.DescriptorProto{Name: new("Unused")})
			},
			missing: "spectre.sample.v1.User.Unused",
		},
		"Imported": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				file := plainDescriptorSet().GetFile()[0]
				file.Name = new("imported.proto")
				file.Package = new("external")
				set.File[len(set.GetFile())-1].Dependency = append(set.GetFile()[len(set.GetFile())-1].GetDependency(), "imported.proto")
				set.File = append(set.GetFile(), file)
			},
			missing: "external.Payload",
		},
	} {
		t.Run(name, func(t *testing.T) {
			modified := sampleDescriptorSet()
			test.mutate(modified)
			registry, err := descriptors.NewRegistry(modified)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, loaded)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `protobuf message "`+test.missing+`" needs a declaration`)
		})
	}
}

func TestAgreementRequiresEveryUnaryMethod(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for _, name := range []string{"spectre.sample.v1.UserService.GetUser", "spectre.sample.v1.UserService.ListUsers"} {
		t.Run(name, func(t *testing.T) {
			operations := slices.DeleteFunc(loaded.Operations(), func(operation schema.Operation) bool { return operation.Name == name })
			missing, err := schema.New(loaded.Types(), operations)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, missing)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `protobuf method "`+name+`" needs a declaration`)
		})
	}
}

func TestAgreementRejectsMismatchedFields(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		typeName string
		fieldName string
		mutate func(field *schema.Field)
		message string
	}{
		"NonPresenceScalar": {typeName: "User", fieldName: "id", mutate: func(field *schema.Field) { field.Optional = true }, message: `field "id" must be required`},
		"OptionalScalar": {typeName: "User", fieldName: "nickname", mutate: func(field *schema.Field) { field.Optional = false }, message: `field "nickname" must be optional`},
		"MessagePresence": {typeName: "User", fieldName: "profile", mutate: func(field *schema.Field) { field.Optional = false }, message: `field "profile" must be optional`},
		"OneofPresence": {typeName: "User", fieldName: "email", mutate: func(field *schema.Field) { field.Optional = false }, message: `field "email" must be optional`},
		"JSONName": {typeName: "User", fieldName: "id", mutate: func(field *schema.Field) { field.Name = "userId" }, message: `field "id" is missing`},
		"Bytes": {typeName: "User", fieldName: "avatar", mutate: func(field *schema.Field) { field.Value = schema.Value{Kind: schema.KindNumber} }, message: `field "avatar" has type number, expected string`},
		"Unsigned64": {typeName: "User", fieldName: "revision", mutate: func(field *schema.Field) { field.Value = schema.Value{Kind: schema.KindNumber} }, message: `field "revision" has type number, expected string`},
		"Signed64": {typeName: "ListUsersResponse", fieldName: "totalCount", mutate: func(field *schema.Field) { field.Value = schema.Value{Kind: schema.KindNumber} }, message: `field "totalCount" has type number, expected string`},
		"Timestamp": {typeName: "User", fieldName: "createdAt", mutate: func(field *schema.Field) { field.Value = schema.Value{Kind: schema.KindNumber} }, message: `field "createdAt" has type number, expected string`},
		"Enum": {typeName: "User", fieldName: "roles", mutate: func(field *schema.Field) {
			value := *field.Value.Element
			value.Literals = value.Literals[:1]
			field.Value.Element = &value
		}, message: `field "roles" has type`},
		"MapElement": {typeName: "User", fieldName: "labels", mutate: func(field *schema.Field) { field.Value.Element = &schema.Value{Kind: schema.KindBoolean} }, message: `field "labels" has type Record<string, boolean>, expected Record<string, string>`},
		"NestedCollection": {typeName: "User.Profile", fieldName: "addresses", mutate: func(field *schema.Field) { field.Value.Kind = schema.KindMap }, message: `field "addresses" has type Record<string, spectre.sample.v1.Address>, expected spectre.sample.v1.Address[]`},
		"ObjectReference": {typeName: "User", fieldName: "profile", mutate: func(field *schema.Field) { field.Value.Type = "spectre.sample.v1.Phone" }, message: `field "profile" has type spectre.sample.v1.Phone, expected spectre.sample.v1.User.Profile`},
	} {
		t.Run(name, func(t *testing.T) {
			types := cloneDeclaredTypes(loaded)
			for _, declared := range types {
				if declared.Name != "spectre.sample.v1."+test.typeName {
					continue
				}
				for index := range declared.Fields {
					if declared.Fields[index].Name == test.fieldName {
						test.mutate(&declared.Fields[index])
					}
				}
			}
			modified, err := schema.New(types, loaded.Operations())
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, modified)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestAgreementRejectsMissingAndExtraFields(t *testing.T) {
	set := plainDescriptorSet()
	set.File[0].MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{{
		Name: new("id"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
	}}
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		fields []schema.Field
		message string
	}{
		"Missing": {message: `field "id" is missing`},
		"Extra": {fields: []schema.Field{{Name: "id", Value: schema.Value{Kind: schema.KindString}}, {Name: "extra", Value: schema.Value{Kind: schema.KindString}}}, message: `field "extra" is not in the protobuf message`},
	} {
		t.Run(name, func(t *testing.T) {
			loaded, err := schema.New([]*schema.Type{{Name: "example.Payload", Fields: test.fields}}, nil)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, loaded)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestAgreementAcceptsEquivalentEnumUnion(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	types := cloneDeclaredTypes(loaded)
	for _, declared := range types {
		if declared.Name != "spectre.sample.v1.User" {
			continue
		}
		for index := range declared.Fields {
			field := &declared.Fields[index]
			if field.Name == "roles" {
				value := *field.Value.Element
				value.Type = ""
				value.Literals = slices.Clone(value.Literals)
				slices.Reverse(value.Literals)
				field.Value.Element = &value
			}
		}
	}
	modified, err := schema.New(types, loaded.Operations())
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, modified))
}

func TestAgreementRejectsOperationTypes(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for _, name := range []string{"Request", "Response"} {
		t.Run(name, func(t *testing.T) {
			operations := loaded.Operations()
			if name == "Request" {
				operations[0].Request = "spectre.sample.v1.User"
			} else {
				operations[0].Response = "spectre.sample.v1.User"
			}
			modified, err := schema.New(loaded.Types(), operations)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, modified)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `operation "spectre.sample.v1.UserService.GetUser" has types`)
		})
	}
}

func TestAgreementAcceptsRawHTTPDeclarations(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	generated["raw.d.ts"] = `declare module "raw" { interface RawRequest { id?: string } interface RawResponse { count: number } interface RawService { Fetch(request: RawRequest): RawResponse } }`
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
	loaded, err = schema.ParseSources(t.Context(), map[string]string{"raw.d.ts": generated["raw.d.ts"]})
	assert.NoError(t, err)
	registry, err = descriptors.NewRegistry(&descriptorpb.FileDescriptorSet{})
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func TestAgreementRejectsDeclaredStreamingMethods(t *testing.T) {
	set := streamingDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for _, name := range []string{"ClientStream", "ServerStream", "BidirectionalStream"} {
		t.Run(name, func(t *testing.T) {
			operation := loaded.Operations()[0]
			operation.Name = "example.users.v1.UserService."+name
			modified, err := schema.New(loaded.Types(), append(loaded.Operations(), operation))
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, modified)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "is a streaming protobuf method")
		})
	}
}

func TestAgreementRejectsScalarMethodTypes(t *testing.T) {
	for _, name := range []string{"Input", "Output"} {
		t.Run(name, func(t *testing.T) {
			set := scalarMethodDescriptorSet(name)
			registry, err := descriptors.NewRegistry(set)
			assert.NoError(t, err)
			loaded, err := schema.New([]*schema.Type{{Name: "example.Payload"}}, nil)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, loaded)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `uses unsupported request or response type "google.protobuf.Timestamp"`)
		})
	}
}

func TestAgreementRejectsObjectDeclarationForWellKnownScalar(t *testing.T) {
	set := scalarDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	generated["wrong.d.ts"] = `declare module "google.protobuf" { interface Timestamp { seconds: string; nanos: number } }`
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	err = descriptors.CheckAgreement(registry, loaded)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `type "google.protobuf.Timestamp" cannot declare a protobuf message without an object JSON shape`)
}

func TestAgreementAllowsUnreferencedUntypedWellKnownMessages(t *testing.T) {
	set := descriptorSetFromFiles(anypb.File_google_protobuf_any_proto, structpb.File_google_protobuf_struct_proto)
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{}, generated)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func cloneDeclaredTypes(loaded *schema.Schema) []*schema.Type {
	types := make([]*schema.Type, 0, len(loaded.Types()))
	for _, declared := range loaded.Types() {
		types = append(types, &schema.Type{Name: declared.Name, Fields: slices.Clone(declared.Fields)})
	}
	return types
}

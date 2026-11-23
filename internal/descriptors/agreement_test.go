package descriptors_test

import (
	"slices"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
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
		mutate  func(set *descriptorpb.FileDescriptorSet)
		missing string
	}{
		"TopLevel": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				file := set.GetFile()[len(set.GetFile())-1]
				file.MessageType = append(file.GetMessageType(), &descriptorpb.DescriptorProto{Name: new("Unused")})
			},
			missing: "spectre.sample.v1.Unused",
		},
		"Nested": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				user := set.GetFile()[1].GetMessageType()[0]
				user.NestedType = append(user.GetNestedType(), &descriptorpb.DescriptorProto{Name: new("Unused")})
			},
			missing: "spectre.sample.v1.User.Unused",
		},
		"SeparateFile": {
			mutate: func(set *descriptorpb.FileDescriptorSet) {
				file := plainDescriptorSet().GetFile()[0]
				file.Name = new("separate.proto")
				file.Package = new("external")
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

func TestAgreementSkipsUnreferencedImport(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)

	// A file imported for its side effects, with a message no payload references.
	imported := plainDescriptorSet().GetFile()[0]
	imported.Name = new("imported.proto")
	imported.Package = new("external")
	last := set.GetFile()[len(set.GetFile())-1]
	last.Dependency = append(last.GetDependency(), "imported.proto")
	set.File = append(set.GetFile(), imported)

	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	// Nothing reaches external.Payload, so it needs no declaration.
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
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
		typeName  schema.TypeRef
		fieldName string
		mutate    func(field *schema.Field, types map[schema.TypeRef]*schema.ObjectType)
		message   string
	}{
		"NonPresenceScalar": {typeName: "User", fieldName: "id", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) { field.Optional = true }, message: `field "id" must be required`},
		"OptionalScalar":    {typeName: "User", fieldName: "nickname", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) { field.Optional = false }, message: `field "nickname" must be optional`},
		"MessagePresence":   {typeName: "User", fieldName: "profile", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) { field.Optional = false }, message: `field "profile" must be optional`},
		"OneofPresence":     {typeName: "User", fieldName: "email", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) { field.Optional = false }, message: `field "email" must be optional`},
		"NullableScalar": {typeName: "User", fieldName: "id", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) {
			field.Type = schema.NullableType{Type: field.Type}
		}, message: `field "id" of a protobuf message cannot be null`},
		"JSONName":   {typeName: "User", fieldName: "id", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) { field.Name = "userId" }, message: `field "id" is missing`},
		"Bytes":      {typeName: "User", fieldName: "avatar", mutate: asNumber, message: `field "avatar" has type number, expected string`},
		"Unsigned64": {typeName: "User", fieldName: "revision", mutate: asNumber, message: `field "revision" has type number, expected string`},
		"Signed64":   {typeName: "ListUsersResponse", fieldName: "totalCount", mutate: asNumber, message: `field "totalCount" has type number, expected string`},
		"Timestamp":  {typeName: "User", fieldName: "createdAt", mutate: asNumber, message: `field "createdAt" has type number, expected string`},
		"Enum": {typeName: "User", fieldName: "roles", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) {
			role := field.Type.(schema.ListType).Element.(schema.NumberType)
			role.Literals = role.Literals[:1]
			field.Type = schema.ListType{Element: role}
		}, message: `field "roles" has type`},
		"MapElement": {typeName: "User", fieldName: "labels", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) {
			field.Type = schema.MapType{Element: schema.BooleanType{}}
		}, message: `field "labels" has type Record<string, boolean>, expected Record<string, string>`},
		"NestedCollection": {typeName: "User.Profile", fieldName: "addresses", mutate: func(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) {
			field.Type = schema.MapType{Element: field.Type.(schema.ListType).Element}
		}, message: `field "addresses" has type Record<string, spectre.sample.v1.Address>, expected spectre.sample.v1.Address[]`},
		"ObjectReference": {typeName: "User", fieldName: "profile", mutate: func(field *schema.Field, types map[schema.TypeRef]*schema.ObjectType) {
			field.Type = types["spectre.sample.v1.Phone"]
		}, message: `field "profile" has type spectre.sample.v1.Phone, expected spectre.sample.v1.User.Profile`},
	} {
		t.Run(name, func(t *testing.T) {
			types := cloneDeclaredTypes(loaded)
			byName := map[schema.TypeRef]*schema.ObjectType{}
			for _, declared := range types {
				byName[declared.Name] = declared
			}
			declared := byName["spectre.sample.v1."+test.typeName]
			for index := range declared.Fields {
				if declared.Fields[index].Name == test.fieldName {
					test.mutate(&declared.Fields[index], byName)
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
		fields  []schema.Field
		message string
	}{
		"Missing": {message: `field "id" is missing`},
		"Extra":   {fields: []schema.Field{{Name: "id", Type: schema.StringType{}}, {Name: "extra", Type: schema.StringType{}}}, message: `field "extra" is not in the protobuf message`},
	} {
		t.Run(name, func(t *testing.T) {
			loaded, err := schema.New([]*schema.ObjectType{{Name: "example.Payload", Fields: test.fields}}, nil)
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
				role := field.Type.(schema.ListType).Element.(schema.NumberType)
				role.Name = None[schema.TypeRef]()
				role.Literals = slices.Clone(role.Literals)
				slices.Reverse(role.Literals)
				field.Type = schema.ListType{Element: role}
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
			operation.Name = "example.users.v1.UserService." + name
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
			loaded, err := schema.New([]*schema.ObjectType{{Name: "example.Payload"}}, nil)
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

// cloneDeclaredTypes copies every object type, pointing fields at the copies so the
// result is a valid input to schema.New.
func cloneDeclaredTypes(loaded *schema.Schema) []*schema.ObjectType {
	originals := loaded.Types()
	clones := make(map[schema.TypeRef]*schema.ObjectType, len(originals))
	types := make([]*schema.ObjectType, 0, len(originals))
	for _, original := range originals {
		clones[original.Name] = &schema.ObjectType{Name: original.Name}
		types = append(types, clones[original.Name])
	}
	for _, original := range originals {
		fields := slices.Clone(original.Fields)
		for index := range fields {
			fields[index].Type = retarget(fields[index].Type, clones)
		}
		clones[original.Name].Fields = fields
	}
	return types
}

func retarget(declared schema.Type, clones map[schema.TypeRef]*schema.ObjectType) schema.Type {
	switch declared := declared.(type) {
	case *schema.ObjectType:
		return clones[declared.Name]
	case schema.ListType:
		return schema.ListType{Element: retarget(declared.Element, clones)}
	case schema.MapType:
		return schema.MapType{Element: retarget(declared.Element, clones)}
	case schema.NullableType:
		return schema.NullableType{Type: retarget(declared.Type, clones)}
	case schema.StringType, schema.NumberType, schema.BooleanType, schema.EnumType, schema.UnknownType:
	}
	return declared
}

func asNumber(field *schema.Field, _ map[schema.TypeRef]*schema.ObjectType) {
	field.Type = schema.NumberType{}
}

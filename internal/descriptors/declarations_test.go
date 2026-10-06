package descriptors_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/block/spectre/internal/descriptors"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/schema"
)

func TestDeclarationsSampleGolden(t *testing.T) {
	generated, err := descriptors.Declarations(t.Context(), sampleDescriptorSet())
	assert.NoError(t, err)
	golden := map[string]string{}
	for _, name := range []string{"users.d.ts", "service.d.ts"} {
		data, err := os.ReadFile(filepath.Join("..", "sample", "schema", name))
		assert.NoError(t, err)
		golden[name] = string(data)
	}
	assert.Equal(t, golden, generated)
}

func TestDeclarationsSampleRoundTrip(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))

	user, err := loaded.Type("spectre.sample.v1.User")
	assert.NoError(t, err)
	text := schema.Value{Kind: schema.KindString}
	assert.Equal(t, &schema.Type{Name: "spectre.sample.v1.User", Fields: []schema.Field{
		{Name: "id", Value: text},
		{Name: "name", Value: text},
		{Name: "nickname", Optional: true, Value: text},
		{Name: "roles", Value: schema.Value{Kind: schema.KindList, Element: &schema.Value{
			Kind: schema.KindNumber, Type: "spectre.sample.v1.Role",
			Literals: []string{"ROLE_ADMIN", "ROLE_EDITOR", "ROLE_READER", "ROLE_UNSPECIFIED"},
		}}},
		{Name: "labels", Value: schema.Value{Kind: schema.KindMap, Element: &text}},
		{Name: "profile", Optional: true, Value: schema.Value{Kind: schema.KindObject, Type: "spectre.sample.v1.User.Profile"}},
		{Name: "avatar", Value: text},
		{Name: "revision", Value: text},
		{Name: "createdAt", Optional: true, Value: text},
		{Name: "email", Optional: true, Value: text},
		{Name: "phone", Optional: true, Value: schema.Value{Kind: schema.KindObject, Type: "spectre.sample.v1.Phone"}},
	}}, user)
	profile, err := loaded.Type("spectre.sample.v1.User.Profile")
	assert.NoError(t, err)
	assert.Equal(t, &schema.Type{Name: "spectre.sample.v1.User.Profile", Fields: []schema.Field{
		{Name: "addresses", Value: schema.Value{Kind: schema.KindList, Element: &schema.Value{Kind: schema.KindObject, Type: "spectre.sample.v1.Address"}}},
		{Name: "marketingConsent", Optional: true, Value: schema.Value{Kind: schema.KindBoolean}},
		{Name: "preferences", Value: schema.Value{Kind: schema.KindMap, Element: &schema.Value{Kind: schema.KindObject, Type: "spectre.sample.v1.Preference"}}},
	}}, profile)
	assert.Equal(t, []schema.Operation{
		{Name: "spectre.sample.v1.UserService.GetUser", Request: "spectre.sample.v1.GetUserRequest", Response: "spectre.sample.v1.GetUserResponse"},
		{Name: "spectre.sample.v1.UserService.ListUsers", Request: "spectre.sample.v1.ListUsersRequest", Response: "spectre.sample.v1.ListUsersResponse"},
	}, loaded.Operations())
}

func TestSampleDeclarationWireJSONRoundTrip(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		message  proto.Message
		expected string
	}{
		"AbsentPresence": {
			message:  &samplepb.User{},
			expected: `{"id":"","name":"","roles":[],"labels":{},"avatar":"","revision":"0"}`,
		},
		"PresentDefaults": {
			message:  &samplepb.User{Nickname: new(""), Profile: &samplepb.User_Profile{MarketingConsent: new(false)}, Contact: &samplepb.User_Email{Email: ""}},
			expected: `{"id":"","name":"","nickname":"","roles":[],"labels":{},"profile":{"addresses":[],"marketingConsent":false,"preferences":{}},"avatar":"","revision":"0","email":""}`,
		},
		"UnknownEnumNumber": {
			message:  &samplepb.User{Roles: []samplepb.Role{samplepb.Role_ROLE_ADMIN, 99}},
			expected: `{"id":"","name":"","roles":["ROLE_ADMIN",99],"labels":{},"avatar":"","revision":"0"}`,
		},
		"NonFiniteFloat": {
			message:  &samplepb.Preference{Value: &samplepb.Preference_Weight{Weight: math.Inf(-1)}},
			expected: `{"weight":"-Infinity"}`,
		},
		"PopulatedCollections": {
			message: &samplepb.ListUsersResponse{
				Users: []*samplepb.User{{
					Id: "u1", Name: "Alice", Nickname: new("Al"), Roles: []samplepb.Role{samplepb.Role_ROLE_ADMIN},
					Labels: map[string]string{"team": "core"}, Avatar: []byte{0, 255}, Revision: 18446744073709551615,
					CreatedAt: &timestamppb.Timestamp{Seconds: 1}, Contact: &samplepb.User_Phone{Phone: &samplepb.Phone{CountryCode: "1", Number: "555", Extension: new("")}},
					Profile: &samplepb.User_Profile{
						Addresses:        []*samplepb.Address{{Lines: []string{"1 Main St"}, City: "Town", PostalCode: "123", CountryCode: "US"}},
						MarketingConsent: new(false), Preferences: map[string]*samplepb.Preference{
							"zero":  {Value: &samplepb.Preference_Weight{Weight: 0}},
							"off":   {Value: &samplepb.Preference_Enabled{Enabled: false}},
							"empty": {Value: &samplepb.Preference_Text{Text: ""}},
						},
					},
				}},
				GeneratedAt: &timestamppb.Timestamp{Seconds: 2}, TotalCount: 9223372036854775807,
			},
			expected: `{"users":[{"id":"u1","name":"Alice","nickname":"Al","roles":["ROLE_ADMIN"],"labels":{"team":"core"},"profile":{"addresses":[{"lines":["1 Main St"],"city":"Town","postalCode":"123","countryCode":"US"}],"marketingConsent":false,"preferences":{"zero":{"weight":0},"off":{"enabled":false},"empty":{"text":""}}},"avatar":"AP8=","revision":"18446744073709551615","createdAt":"1970-01-01T00:00:01Z","phone":{"countryCode":"1","number":"555","extension":""}}],"generatedAt":"1970-01-01T00:00:02Z","totalCount":"9223372036854775807"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			jsonOptions := protojson.MarshalOptions{EmitDefaultValues: true}
			jsonData, err := jsonOptions.Marshal(test.message)
			assert.NoError(t, err)
			binaryData, err := proto.Marshal(test.message)
			assert.NoError(t, err)
			messageName := test.message.ProtoReflect().Descriptor().FullName()
			message, err := registry.Message(messageName)
			assert.NoError(t, err)
			fromBinary := dynamicpb.NewMessage(message)
			assert.NoError(t, proto.Unmarshal(binaryData, fromBinary))
			binaryJSON, err := jsonOptions.Marshal(fromBinary)
			assert.NoError(t, err)
			fromJSON := dynamicpb.NewMessage(message)
			assert.NoError(t, protojson.Unmarshal(jsonData, fromJSON))
			assert.True(t, proto.Equal(fromBinary, fromJSON))
			expected := decodeJSON(t, []byte(test.expected))
			for _, data := range [][]byte{jsonData, binaryJSON, []byte(test.expected)} {
				actual := decodeJSON(t, data)
				assert.Equal(t, expected, actual)
				assert.NoError(t, loaded.Validate(string(messageName), actual))
			}
		})
	}
}

func TestDeclarationsWellKnownScalars(t *testing.T) {
	set := scalarDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	declared, err := loaded.Type("example.Payload")
	assert.NoError(t, err)
	text := schema.Value{Kind: schema.KindString}
	number := schema.Value{Kind: schema.KindNumber}
	float := schema.Value{Kind: schema.KindNumber, Literals: []string{"-Infinity", "Infinity", "NaN"}}
	assert.Equal(t, &schema.Type{Name: "example.Payload", Fields: []schema.Field{
		{Name: "timestamp", Optional: true, Value: text},
		{Name: "duration", Optional: true, Value: text},
		{Name: "fieldMask", Optional: true, Value: text},
		{Name: "stringValue", Optional: true, Value: text},
		{Name: "bytesValue", Optional: true, Value: text},
		{Name: "int64Value", Optional: true, Value: text},
		{Name: "uint64Value", Optional: true, Value: text},
		{Name: "doubleValue", Optional: true, Value: float},
		{Name: "floatValue", Optional: true, Value: float},
		{Name: "int32Value", Optional: true, Value: number},
		{Name: "uint32Value", Optional: true, Value: number},
		{Name: "boolValue", Optional: true, Value: schema.Value{Kind: schema.KindBoolean}},
	}}, declared)
	assert.Equal(t, 1, len(generated))
}

func TestDeclarationsAcceptNumbersForOpenEnums(t *testing.T) {
	for syntax, kind := range map[string]schema.Kind{"proto2": schema.KindEnum, "proto3": schema.KindNumber} {
		t.Run(syntax, func(t *testing.T) {
			set := plainDescriptorSet()
			file := set.GetFile()[0]
			file.Syntax = new(syntax)
			file.EnumType = []*descriptorpb.EnumDescriptorProto{{Name: new("State"), Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("STATE_UNSPECIFIED"), Number: new(int32(0))},
				{Name: new("STATE_ACTIVE"), Number: new(int32(1))},
			}}}
			file.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{{
				Name: new("state"), JsonName: new("state"), Number: new(int32(1)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: new(".example.State"),
			}}
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.NoError(t, err)
			loaded, err := schema.ParseSources(t.Context(), generated)
			assert.NoError(t, err)
			declared, err := loaded.Type("example.Payload")
			assert.NoError(t, err)
			state := schema.Value{Kind: kind, Literals: []string{"STATE_ACTIVE", "STATE_UNSPECIFIED"}, Type: "example.State"}
			assert.Equal(t, &schema.Type{Name: "example.Payload", Fields: []schema.Field{
				{Name: "state", Optional: syntax == "proto2", Value: state},
			}}, declared)
		})
	}
}

func TestDeclarationsAcceptExtensions(t *testing.T) {
	set := plainDescriptorSet()
	file := set.GetFile()[0]
	file.Syntax = new("proto2")
	file.MessageType[0].ExtensionRange = []*descriptorpb.DescriptorProto_ExtensionRange{{Start: new(int32(100)), End: new(int32(200))}}
	extension := func(name string, number int32, label descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: new(name), Number: new(number), Label: label.Enum(),
			Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Extendee: new(".example.Payload"),
		}
	}
	file.Extension = []*descriptorpb.FieldDescriptorProto{extension("tags", 101, descriptorpb.FieldDescriptorProto_LABEL_REPEATED)}
	file.MessageType = append(file.GetMessageType(), &descriptorpb.DescriptorProto{
		Name: new("Holder"), Extension: []*descriptorpb.FieldDescriptorProto{extension("note", 100, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
	})
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	declared, err := loaded.Type("example.Payload")
	assert.NoError(t, err)
	assert.Equal(t, &schema.Type{Name: "example.Payload", Fields: []schema.Field{
		{Name: "[example.Holder.note]", Optional: true, Value: schema.Value{Kind: schema.KindString}},
		{Name: "[example.tags]", Optional: true, Value: schema.Value{Kind: schema.KindList, Element: &schema.Value{Kind: schema.KindString}}},
	}}, declared)

	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	message, err := registry.Message("example.Payload")
	assert.NoError(t, err)
	payload := dynamicpb.NewMessage(message)
	for _, name := range []protoreflect.FullName{"example.Holder.note", "example.tags"} {
		extensionType, err := registry.Types().FindExtensionByName(name)
		assert.NoError(t, err)
		field := extensionType.TypeDescriptor()
		value := protoreflect.ValueOfString("a")
		if field.IsList() {
			list := payload.NewField(field).List()
			list.Append(value)
			value = protoreflect.ValueOfList(list)
		}
		payload.Set(field, value)
	}
	data, err := (protojson.MarshalOptions{Resolver: registry.Types(), EmitDefaultValues: true}).Marshal(payload)
	assert.NoError(t, err)
	assert.NoError(t, loaded.Validate("example.Payload", decodeJSON(t, data)))
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func TestDeclarationsSkipsDescriptorProto(t *testing.T) {
	// A custom option extending FieldOptions pulls google/protobuf/descriptor.proto
	// into the set, the way real dependency protos do.
	set := descriptorSetFromFiles(descriptorpb.File_google_protobuf_descriptor_proto)
	payload := plainDescriptorSet().GetFile()[0]
	payload.Syntax = new("proto2")
	payload.Dependency = []string{"google/protobuf/descriptor.proto"}
	payload.Extension = []*descriptorpb.FieldDescriptorProto{{
		Name: new("required"), Number: new(int32(50000)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), Extendee: new(".google.protobuf.FieldOptions"),
	}}
	set.File = append(set.GetFile(), payload)

	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	// descriptor.proto describes protobuf options, never a payload, so it gets no declaration.
	_, hasDescriptor := generated["google/protobuf/descriptor.d.ts"]
	assert.False(t, hasDescriptor)
	_, hasPayload := generated["payload.d.ts"]
	assert.True(t, hasPayload)

	// Agreement holds without a descriptor.proto declaration, as it must when the proxy loads the schema.
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func TestDeclarationsKeepsReferencedDescriptorProto(t *testing.T) {
	// Here a payload field is typed as a descriptor.proto message, so its
	// declaration is a real request type, not just option plumbing.
	set := descriptorSetFromFiles(descriptorpb.File_google_protobuf_descriptor_proto)
	payload := plainDescriptorSet().GetFile()[0]
	payload.Dependency = []string{"google/protobuf/descriptor.proto"}
	payload.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{
		messageField("schema", 1, ".google.protobuf.DescriptorProto"),
	}
	set.File = append(set.GetFile(), payload)

	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	// The payload uses descriptor.proto, so its declaration must be generated.
	_, hasDescriptor := generated["google/protobuf/descriptor.d.ts"]
	assert.True(t, hasDescriptor)
	_, hasPayload := generated["payload.d.ts"]
	assert.True(t, hasPayload)

	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func TestDeclarationsSkipsOptionValueMessage(t *testing.T) {
	set := descriptorSetFromFiles(descriptorpb.File_google_protobuf_descriptor_proto)
	// annotations.proto defines a message used only as the value of a custom option.
	annotation := &descriptorpb.FileDescriptorProto{
		Name: new("annotations.proto"), Package: new("ann"), Syntax: new("proto2"),
		Dependency:  []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{Name: new("Annotation")}},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name: new("note"), Number: new(int32(50000)), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: new(".ann.Annotation"), Extendee: new(".google.protobuf.FieldOptions"),
		}},
	}
	payload := plainDescriptorSet().GetFile()[0]
	payload.Dependency = []string{"annotations.proto"}
	set.File = append(set.GetFile(), annotation, payload)

	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	// Nothing uses Annotation as a payload, so its file gets no declaration.
	_, hasAnnotation := generated["annotations.d.ts"]
	assert.False(t, hasAnnotation)
	_, hasDescriptor := generated["google/protobuf/descriptor.d.ts"]
	assert.False(t, hasDescriptor)
	_, hasPayload := generated["payload.d.ts"]
	assert.True(t, hasPayload)

	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
}

func TestDeclarationsRejectUnsupportedWellKnownReferences(t *testing.T) {
	for _, name := range []string{"Any", "Struct", "Value", "ListValue", "NullValue"} {
		t.Run(name, func(t *testing.T) {
			set := untypedDescriptorSet(name)
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "google.protobuf."+name+" is not supported")
			assert.Equal(t, map[string]string(nil), generated)
			registry, err := descriptors.NewRegistry(set)
			assert.NoError(t, err)
			loaded, err := schema.New(nil, nil)
			assert.NoError(t, err)
			err = descriptors.CheckAgreement(registry, loaded)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "google.protobuf."+name+" is not supported")
		})
	}
}

func TestDeclarationsRejectInvalidSets(t *testing.T) {
	for name, set := range map[string]*descriptorpb.FileDescriptorSet{
		"Nil":           nil,
		"MissingImport": {File: newDescriptorSet().GetFile()[:1]},
	} {
		t.Run(name, func(t *testing.T) {
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.Error(t, err)
			assert.Equal(t, map[string]string(nil), generated)
		})
	}
}

func TestDeclarationsRejectUnsafePaths(t *testing.T) {
	for name, path := range map[string]string{
		"Traversal":          "../outside.proto",
		"NestedTraversal":    "nested/../../outside.proto",
		"Absolute":           "/outside.proto",
		"BackslashTraversal": `..\outside.proto`,
		"Drive":              `C:/outside.proto`,
		"CommentInjection":   "outside.proto\ninterface Malicious {}",
		"NotCanonical":       "nested/../outside.proto",
	} {
		t.Run(name, func(t *testing.T) {
			set := plainDescriptorSet()
			set.File[0].Name = new(path)
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "not a safe relative path")
			assert.Equal(t, map[string]string(nil), generated)
			assert.Error(t, descriptors.WriteDeclarations(t.Context(), set, t.TempDir()))
		})
	}
}

func TestDeclarationsRejectFilesWithoutPackage(t *testing.T) {
	set := plainDescriptorSet()
	set.File[0].Package = nil
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `render "payload.proto": proto files must declare a package`)
	assert.Equal(t, map[string]string(nil), generated)
}

func TestDeclarationsRejectOutputCollisions(t *testing.T) {
	set := plainDescriptorSet()
	other := proto.Clone(set.GetFile()[0]).(*descriptorpb.FileDescriptorProto)
	other.Name = new("payload")
	other.MessageType[0].Name = new("Other")
	set.File = append(set.GetFile(), other)
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "multiple proto files write declaration")
	assert.Equal(t, map[string]string(nil), generated)
}

func TestDeclarationsRejectNamesThatCannotRoundTrip(t *testing.T) {
	for name, messageName := range map[string]string{"Keyword": "class"} {
		t.Run(name, func(t *testing.T) {
			set := plainDescriptorSet()
			message := set.GetFile()[0].GetMessageType()[0]
			message.Name = new(messageName)
			message.Field = []*descriptorpb.FieldDescriptorProto{messageField("next", 1, ".example."+messageName)}
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "generated declarations")
			assert.Equal(t, map[string]string(nil), generated)
		})
	}
}

func TestDeclarationsQuotesJSONNames(t *testing.T) {
	set := plainDescriptorSet()
	set.File[0].MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{{
		Name: new("value"), JsonName: new(`odd"key-name`), Number: new(int32(1)),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
	}}
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	assert.Contains(t, generated["payload.d.ts"], `"odd\"key-name": string;`)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	actual, err := loaded.Type("example.Payload")
	assert.NoError(t, err)
	assert.Equal(t, &schema.Type{Name: "example.Payload", Fields: []schema.Field{{Name: `odd"key-name`, Value: schema.Value{Kind: schema.KindString}}}}, actual)
}

func TestWriteDeclarationsPreservesUnrelatedFiles(t *testing.T) {
	set := plainDescriptorSet()
	set.File[0].Name = new("nested/payload.proto")
	dir := filepath.Join(t.TempDir(), "generated")
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "nested", "payload.d.ts"), []byte("stale"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "handwritten.d.ts"), []byte("interface Raw {}"), 0o600))
	assert.NoError(t, descriptors.WriteDeclarations(t.Context(), set, dir))
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	for name, expected := range generated {
		actual, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		assert.NoError(t, err)
		assert.Equal(t, expected, string(actual))
	}
	untouched, err := os.ReadFile(filepath.Join(dir, "handwritten.d.ts"))
	assert.NoError(t, err)
	assert.Equal(t, "interface Raw {}", string(untouched))
}

func TestWriteDeclarationsRejectsEscapingSymlinks(t *testing.T) {
	for _, name := range []string{"Directory", "File"} {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, "payload.d.ts")
			assert.NoError(t, os.WriteFile(outsideFile, []byte("untouched"), 0o600))
			dir := t.TempDir()
			set := plainDescriptorSet()
			if name == "Directory" {
				set.File[0].Name = new("nested/payload.proto")
				assert.NoError(t, os.Symlink(outside, filepath.Join(dir, "nested")))
			} else {
				assert.NoError(t, os.Symlink(outsideFile, filepath.Join(dir, "payload.d.ts")))
			}
			assert.Error(t, descriptors.WriteDeclarations(t.Context(), set, dir))
			actual, err := os.ReadFile(outsideFile)
			assert.NoError(t, err)
			assert.Equal(t, "untouched", string(actual))
		})
	}
}

func TestDeclarationsOmitsStreamingMethods(t *testing.T) {
	set := streamingDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	assert.Equal(t, []schema.Operation{{Name: "example.users.v1.UserService.ListUsers", Request: "example.users.v1.ListUsersRequest", Response: "example.users.v1.ListUsersResponse"}}, loaded.Operations())
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, descriptors.CheckAgreement(registry, loaded))
	for _, name := range []string{"ClientStream", "ServerStream", "BidirectionalStream"} {
		assert.NotContains(t, generated["service.d.ts"], name)
	}
}

func TestDeclarationsRejectScalarMethodTypes(t *testing.T) {
	for _, name := range []string{"Input", "Output"} {
		t.Run(name, func(t *testing.T) {
			set := scalarMethodDescriptorSet(name)
			generated, err := descriptors.Declarations(t.Context(), set)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `uses unsupported request or response type "google.protobuf.Timestamp"`)
			assert.Equal(t, map[string]string(nil), generated)
		})
	}
}

func sampleDescriptorSet() *descriptorpb.FileDescriptorSet {
	return descriptorSetFromFiles(samplepb.File_users_proto, samplepb.File_service_proto)
}

func descriptorSetFromFiles(files ...protoreflect.FileDescriptor) *descriptorpb.FileDescriptorSet {
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var include func(file protoreflect.FileDescriptor)
	include = func(file protoreflect.FileDescriptor) {
		if seen[file.Path()] {
			return
		}
		seen[file.Path()] = true
		for index := range file.Imports().Len() {
			include(file.Imports().Get(index).FileDescriptor)
		}
		set.File = append(set.GetFile(), protodesc.ToFileDescriptorProto(file))
	}
	for _, file := range files {
		include(file)
	}
	return set
}

func plainDescriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: new("payload.proto"), Package: new("example"), Syntax: new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: new("Payload")}},
	}}}
}

func scalarDescriptorSet() *descriptorpb.FileDescriptorSet {
	set := descriptorSetFromFiles(timestamppb.File_google_protobuf_timestamp_proto, durationpb.File_google_protobuf_duration_proto,
		fieldmaskpb.File_google_protobuf_field_mask_proto, wrapperspb.File_google_protobuf_wrappers_proto)
	payload := plainDescriptorSet().GetFile()[0]
	payload.Dependency = []string{"google/protobuf/timestamp.proto", "google/protobuf/duration.proto", "google/protobuf/field_mask.proto", "google/protobuf/wrappers.proto"}
	for index, name := range []string{"Timestamp", "Duration", "FieldMask", "StringValue", "BytesValue", "Int64Value", "UInt64Value", "DoubleValue", "FloatValue", "Int32Value", "UInt32Value", "BoolValue"} {
		jsonName := strings.ToLower(name[:1]) + name[1:]
		if name == "UInt64Value" || name == "UInt32Value" {
			jsonName = "uint" + name[4:]
		}
		payload.MessageType[0].Field = append(payload.GetMessageType()[0].GetField(), messageField(jsonName, int32(index+1), ".google.protobuf."+name))
	}
	set.File = append(set.GetFile(), payload)
	return set
}

func untypedDescriptorSet(name string) *descriptorpb.FileDescriptorSet {
	file := structpb.File_google_protobuf_struct_proto
	if name == "Any" {
		file = anypb.File_google_protobuf_any_proto
	}
	set := descriptorSetFromFiles(file)
	payload := plainDescriptorSet().GetFile()[0]
	payload.Dependency = []string{file.Path()}
	field := messageField("value", 1, ".google.protobuf."+name)
	if name == "NullValue" {
		field.Type = descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum()
	}
	payload.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{field}
	set.File = append(set.GetFile(), payload)
	return set
}

func messageField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{Name: new(name), JsonName: new(name), Number: new(number),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: new(typeName)}
}

func streamingDescriptorSet() *descriptorpb.FileDescriptorSet {
	set := newDescriptorSet()
	service := set.GetFile()[0].GetService()[0]
	for _, test := range []struct {
		name           string
		client, server bool
	}{
		{name: "ClientStream", client: true},
		{name: "ServerStream", server: true},
		{name: "BidirectionalStream", client: true, server: true},
	} {
		method := proto.Clone(service.GetMethod()[0]).(*descriptorpb.MethodDescriptorProto)
		method.Name = new(test.name)
		method.ClientStreaming = new(test.client)
		method.ServerStreaming = new(test.server)
		service.Method = append(service.GetMethod(), method)
	}
	return set
}

func scalarMethodDescriptorSet(side string) *descriptorpb.FileDescriptorSet {
	set := descriptorSetFromFiles(timestamppb.File_google_protobuf_timestamp_proto)
	file := plainDescriptorSet().GetFile()[0]
	file.Dependency = []string{"google/protobuf/timestamp.proto"}
	method := &descriptorpb.MethodDescriptorProto{Name: new("Get"), InputType: new(".example.Payload"), OutputType: new(".example.Payload")}
	if side == "Input" {
		method.InputType = new(".google.protobuf.Timestamp")
	} else {
		method.OutputType = new(".google.protobuf.Timestamp")
	}
	file.Service = []*descriptorpb.ServiceDescriptorProto{{Name: new("Service"), Method: []*descriptorpb.MethodDescriptorProto{method}}}
	set.File = append(set.GetFile(), file)
	return set
}

func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	assert.NoError(t, json.Unmarshal(data, &value))
	return value
}

func TestDeclarationsTypeDoesNotShadowNamespace(t *testing.T) {
	set := plainDescriptorSet()
	message := set.GetFile()[0].GetMessageType()[0]
	message.Name = new("example")
	message.Field = []*descriptorpb.FieldDescriptorProto{messageField("next", 1, ".example.example")}
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	declared, err := loaded.Type("example.example")
	assert.NoError(t, err)
	assert.Equal(t, &schema.Type{Name: "example.example", Fields: []schema.Field{
		{Name: "next", Optional: true, Value: schema.Value{Kind: schema.KindObject, Type: "example.example"}},
	}}, declared)
}

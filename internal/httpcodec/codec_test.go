package httpcodec_test

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpcodec"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/schema"
)

func TestWireFormatsShareDeclaredJSONShape(t *testing.T) {
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(samplepb.File_service_proto),
		protodesc.ToFileDescriptorProto(samplepb.File_users_proto),
		protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto),
	}}
	sources, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), sources)
	assert.NoError(t, err)
	codec, err := httpcodec.New(loaded, set)
	assert.NoError(t, err)
	message := &samplepb.ListUsersResponse{
		Users: []*samplepb.User{{
			Id: "one", Nickname: new(""), Roles: []samplepb.Role{samplepb.Role_ROLE_ADMIN},
			Labels: map[string]string{"team": "platform"}, Avatar: []byte{0, 255}, Revision: 9007199254740993,
			Profile: &samplepb.User_Profile{MarketingConsent: new(false), Preferences: map[string]*samplepb.Preference{
				"enabled": {Value: &samplepb.Preference_Enabled{Enabled: false}},
			}},
		}},
		TotalCount: 9007199254740993,
	}
	binaryBody, err := proto.Marshal(message)
	assert.NoError(t, err)
	jsonBody, err := protojson.Marshal(message)
	assert.NoError(t, err)
	frame := []byte{0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(frame[1:], uint32(len(binaryBody)))
	frame = append(frame, binaryBody...)
	var expected any
	assert.NoError(t, json.Unmarshal([]byte(`{
 "users":[{"id":"one","name":"","nickname":"","roles":["ROLE_ADMIN"],"labels":{"team":"platform"},
 "avatar":"AP8=","revision":"9007199254740993","profile":{"addresses":[],"marketingConsent":false,
 "preferences":{"enabled":{"enabled":false}}}}],"totalCount":"9007199254740993"
}`), &expected))
	for name, test := range map[string]struct {
		format httpcodec.Format
		body   []byte
	}{
		"ProtoJSON": {format: httpcodec.ConnectJSON, body: jsonBody},
		"Protobuf":  {format: httpcodec.Protobuf, body: binaryBody},
		"GRPC":      {format: httpcodec.GRPC, body: frame},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := codec.Decode("spectre.sample.v1.ListUsersResponse", test.format, http.Header{}, test.body, 1<<20)
			assert.NoError(t, err)
			assert.Equal(t, expected, decoded)
			assert.NoError(t, loaded.Validate("spectre.sample.v1.ListUsersResponse", decoded))
		})
	}
	// Unknown wire fields must not disappear during JSON conversion.
	unknown := protowire.AppendTag(binaryBody, 100, protowire.VarintType)
	unknown = protowire.AppendVarint(unknown, 1)
	_, err = codec.Decode("spectre.sample.v1.ListUsersResponse", httpcodec.Protobuf, http.Header{}, unknown, 1<<20)
	assert.EqualError(t, err, "protobuf payload contains unknown fields")
}

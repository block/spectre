package message_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/protocols/http/message"
	"github.com/block/spectre/internal/schema"
)

func TestDecodesSuccessfulSourceResponses(t *testing.T) {
	decoder := newRawDecoder(t)
	for name, test := range map[string]struct {
		response message.Response
		message  Option[typedValue]
		failure  string
	}{
		"Success": {
			response: httpResponse(http.StatusOK, "application/json", `{"id":"one","count":2}`),
			message:  Some(typedValue{declared: "raw.Item", value: map[string]any{"id": "one", "count": 2.0}}),
		},
		"ErrorStatus": {
			response: httpResponse(http.StatusNotFound, "application/json", `{"id":"one"}`),
			failure:  "response has status 404",
		},
		"Invalid": {
			response: httpResponse(http.StatusOK, "application/json", `{"count":2}`),
			failure:  "decode response as raw.Item",
		},
	} {
		t.Run(name, func(t *testing.T) {
			content := decoder.SourceResponse(http.MethodGet, http.Header{}, "raw.Item", test.response)

			if test.failure == "" {
				assert.Equal(t, None[error](), content.Failure)
				assert.Equal(t, test.message, typedValues(t, content.Message))
				return
			}
			failure, failed := content.Failure.Get()
			assert.True(t, failed)
			assert.Contains(t, failure.Error(), test.failure)
		})
	}
}

func TestEncodesAnswers(t *testing.T) {
	decoder := newRawDecoder(t)
	loaded, err := schema.New([]*schema.ObjectType{{Name: "raw.Item", Fields: []schema.Field{{Name: "id", Type: schema.StringType{}}}}}, nil)
	assert.NoError(t, err)
	itemType, err := loaded.Type("raw.Item")
	assert.NoError(t, err)
	item, err := schema.Decode(itemType, []byte(`{"id":"one"}`))
	assert.NoError(t, err)
	text, err := schema.As(schema.StringType{}, schema.NewString("one"))
	assert.NoError(t, err)

	answer, err := decoder.Answer(http.Header{}, item)
	assert.NoError(t, err)
	assert.Equal(t, message.Answer{Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"id":"one"}`)}, answer)

	_, err = decoder.Answer(http.Header{"Content-Type": {"application/grpc"}}, item)
	assert.EqualError(t, err, "gRPC requests cannot be answered")

	_, err = decoder.Answer(http.Header{"Content-Type": {"application/x-protobuf"}}, text)
	assert.EqualError(t, err, "encode answer as string: string is not a protobuf message type")
}

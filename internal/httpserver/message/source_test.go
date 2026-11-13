package message_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpserver/message"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

func TestDecodesSuccessfulSourceResponses(t *testing.T) {
	decoder := newRawDecoder(t)
	item := Some[schema.TypeRef]("raw.Item")
	for name, test := range map[string]struct {
		response message.Response
		message  Option[protocol.Message]
		failure  string
	}{
		"Success": {
			response: httpResponse(http.StatusOK, "application/json", `{"id":"one","count":2}`),
			message:  Some(protocol.Message{Type: item, Value: map[string]any{"id": "one", "count": 2.0}}),
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
				assert.Equal(t, test.message, content.Message)
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
	item := Some[schema.TypeRef]("raw.Item")

	answer, err := decoder.Answer(http.Header{}, protocol.Message{Type: item, Value: map[string]any{"id": "one"}})
	assert.NoError(t, err)
	assert.Equal(t, message.Answer{Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"id":"one"}`)}, answer)

	_, err = decoder.Answer(http.Header{"Content-Type": {"application/grpc"}}, protocol.Message{Type: item, Value: map[string]any{"id": "one"}})
	assert.EqualError(t, err, "gRPC requests cannot be answered")

	_, err = decoder.Answer(http.Header{}, protocol.Message{Type: None[schema.TypeRef](), Value: "one"})
	assert.EqualError(t, err, "answer has no type")
}

// Package httpcodec decodes gRPC, Connect, and bare protobuf payloads to JSON.
package httpcodec

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/schema"
)

// Format identifies the protobuf wire representation.
type Format int

const (
	// ConnectJSON is unframed ProtoJSON.
	ConnectJSON Format = iota + 1
	// GRPC is one unary gRPC frame.
	GRPC
	// Protobuf is a bare protobuf message.
	Protobuf
)

// Codec owns the descriptors used only for protobuf wire decoding.
type Codec struct {
	registry *descriptors.Registry
}

// New constructs a codec and checks that the declarations match its descriptors.
func New(loaded *schema.Schema, set *descriptorpb.FileDescriptorSet) (*Codec, error) {
	registry, err := descriptors.NewRegistry(set)
	if err != nil {
		return nil, errors.Wrap(err, "load wire descriptors")
	}
	if err := descriptors.CheckAgreement(registry, loaded); err != nil {
		return nil, errors.Wrap(err, "check wire declarations")
	}
	return &Codec{registry: registry}, nil
}

// Streaming reports whether name refers to a streaming protobuf method.
func (c *Codec) Streaming(name string) bool {
	method, err := c.registry.Method(protoreflect.FullName(name))
	return err == nil && (method.IsStreamingClient() || method.IsStreamingServer())
}

// Decode converts a payload into the ProtoJSON shape of its declared type.
func (c *Codec) Decode(name string, format Format, header http.Header, body []byte, maxBodyBytes int) (any, error) {
	descriptor, err := c.registry.Message(protoreflect.FullName(name))
	if err != nil {
		return nil, errors.Wrap(err, "resolve wire type")
	}
	message := dynamicpb.NewMessage(descriptor)
	switch format {
	case ConnectJSON:
		if err := unmarshalProtoJSON(c.registry, body, message); err != nil {
			return nil, err
		}
	case GRPC, Protobuf:
		payload := body
		if format == GRPC {
			payload, err = grpcPayload(header, body, maxBodyBytes)
			if err != nil {
				return nil, errors.Wrap(err, "read gRPC frame")
			}
		}
		if err := (proto.UnmarshalOptions{Resolver: c.registry.Types()}).Unmarshal(payload, message); err != nil {
			return nil, errors.Wrap(err, "decode protobuf")
		}
		// ProtoJSON silently discards unknown wire fields, which could hide divergence.
		if containsUnknown(message) {
			return nil, errors.New("protobuf payload contains unknown fields")
		}
	default:
		return nil, errors.Errorf("unknown wire format: %d", format)
	}
	return payloadValue(c.registry, message, maxBodyBytes)
}

func grpcPayload(header http.Header, body []byte, maxBodyBytes int) ([]byte, error) {
	if len(body) < 5 {
		return nil, errors.New("missing gRPC message frame")
	}
	length := int(binary.BigEndian.Uint32(body[1:5]))
	if length != len(body)-5 {
		return nil, errors.New("gRPC body must contain exactly one message")
	}
	payload := body[5:]
	switch body[0] {
	case 0:
		return payload, nil
	case 1:
		if header.Get("Grpc-Encoding") != "gzip" {
			return nil, errors.New("unsupported gRPC compression")
		}
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, errors.Wrap(err, "open gzip body")
		}
		decompressed, err := io.ReadAll(io.LimitReader(reader, int64(maxBodyBytes)+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, errors.Wrap(err, "decompress gRPC body")
		}
		if closeErr != nil {
			return nil, errors.Wrap(closeErr, "close gzip body")
		}
		if len(decompressed) > maxBodyBytes {
			return nil, errors.New("decompressed gRPC body exceeds the size limit")
		}
		return decompressed, nil
	default:
		return nil, errors.New("invalid gRPC compression flag")
	}
}

func unmarshalProtoJSON(loaded *descriptors.Registry, data []byte, message proto.Message) error {
	err := (protojson.UnmarshalOptions{Resolver: loaded.Types()}).Unmarshal(data, message)
	return errors.Wrap(err, "decode ProtoJSON")
}

// payloadValue converts a decoded message to the ProtoJSON value normalisers receive.
func payloadValue(loaded *descriptors.Registry, message proto.Message, maxBodyBytes int) (any, error) {
	data, err := (protojson.MarshalOptions{Resolver: loaded.Types(), EmitDefaultValues: true}).Marshal(message)
	if err != nil {
		return nil, errors.Wrap(err, "encode ProtoJSON")
	}
	if len(data) > maxBodyBytes {
		return nil, errors.New("decoded payload exceeds the comparison size limit")
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.Wrap(err, "decode ProtoJSON value")
	}
	return payload, nil
}

func containsUnknown(message protoreflect.Message) bool {
	if len(message.GetUnknown()) > 0 {
		return true
	}
	unknown := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsMap():
			if field.MapValue().Kind() == protoreflect.MessageKind {
				value.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
					unknown = containsUnknown(item.Message())
					return !unknown
				})
			}
		case field.IsList():
			if field.Kind() == protoreflect.MessageKind {
				list := value.List()
				for index := range list.Len() {
					if containsUnknown(list.Get(index).Message()) {
						unknown = true
						break
					}
				}
			}
		case field.Kind() == protoreflect.MessageKind:
			unknown = containsUnknown(value.Message())
		}
		return !unknown
	})
	return unknown
}

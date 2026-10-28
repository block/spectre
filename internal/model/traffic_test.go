package model_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
)

func TestIOKindString(t *testing.T) {
	assert.Equal(t, "ingress[http]", model.IOKind{Direction: model.Ingress, Protocol: model.ProtocolHTTP}.String())
}

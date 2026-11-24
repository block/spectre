package event

import (
	"context"
	"encoding/json"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/schema"
)

// mapper decodes event payloads and runs the scripts' mapping functions.
type mapper struct {
	scripts *comparison.Scripts
	handle  script.Handle
	schema  *schema.Schema
}

func newMapper(scripts *comparison.Scripts, handle script.Handle, loaded *schema.Schema) *mapper {
	return &mapper{scripts: scripts, handle: handle, schema: loaded}
}

// decode decodes a payload as typeName, or untyped without one. An empty payload is JSON null.
func (m *mapper) decode(data []byte, typeName Option[schema.TypeRef]) (schema.Value, error) {
	if len(data) == 0 {
		data = []byte("null")
	}
	name, typed := typeName.Get()
	if !typed {
		return errors.WithStack2(schema.Decode(schema.UnknownType{}, data))
	}
	declared, err := m.schema.Type(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	value, err := schema.Decode(declared, data)
	return value, errors.Wrapf(err, "decode %s", name)
}

// mapValue runs a mapping function in a fresh evaluator.
func (m *mapper) mapValue(ctx context.Context, mapping script.Mapping, data []byte) (schema.Value, error) {
	var mapped schema.Value
	err := m.scripts.Evaluate(ctx, func(evaluator *javascript.Evaluator) error {
		var mapErr error
		mapped, mapErr = m.handle.Instance(evaluator).Map(mapping, data)
		return errors.WithStack(mapErr)
	})
	return mapped, errors.WithStack(err)
}

// mapJSON runs a mapping function and encodes its result as JSON.
func (m *mapper) mapJSON(ctx context.Context, mapping script.Mapping, data []byte) ([]byte, error) {
	mapped, err := m.mapValue(ctx, mapping, data)
	if err != nil {
		return nil, err
	}
	return errors.WithStack2(json.Marshal(mapped))
}

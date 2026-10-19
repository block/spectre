package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/alecthomas/errors"
)

// templateHandler writes records as JSON lines shaped by a Template.
type templateHandler struct {
	template *JSONTemplate
	level    slog.Leveler
	output   io.Writer
	// lock is shared with handlers derived by With, which write to the same output.
	lock   *sync.Mutex
	scopes []handlerScope
}

// handlerScope is one With or WithGroup call. Attributes added after a group nest inside it.
type handlerScope struct {
	group string
	attrs []slog.Attr
}

func newTemplateHandler(template *JSONTemplate, level slog.Leveler, output io.Writer) *templateHandler {
	return &templateHandler{template: template, level: level, output: output, lock: &sync.Mutex{}}
}

// Enabled reports whether level meets the configured minimum.
func (h *templateHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle writes record as one line.
func (h *templateHandler) Handle(_ context.Context, record slog.Record) error {
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	// Match slog's JSON handler, which leaves <, > and & unescaped.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(h.template.render(record, attrsMap(h.attrs(record)))); err != nil {
		return errors.Wrap(err, "encode log record")
	}
	h.lock.Lock()
	defer h.lock.Unlock()
	_, err := h.output.Write(line.Bytes())
	return errors.WithStack(err)
}

// WithAttrs returns a handler that adds attrs to every record.
func (h *templateHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.withScope(handlerScope{attrs: attrs})
}

// WithGroup returns a handler that nests later attributes under name.
func (h *templateHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.withScope(handlerScope{group: name})
}

func (h *templateHandler) withScope(scope handlerScope) *templateHandler {
	derived := *h
	derived.scopes = append(slices.Clip(h.scopes), scope)
	return &derived
}

// attrs nests the handler's and record's attributes under the open groups, working
// outwards so each group wraps everything added after it.
func (h *templateHandler) attrs(record slog.Record) []slog.Attr {
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})
	for _, scope := range slices.Backward(h.scopes) {
		if scope.group != "" {
			attrs = []slog.Attr{{Key: scope.group, Value: slog.GroupValue(attrs...)}}
		} else {
			attrs = append(slices.Clone(scope.attrs), attrs...)
		}
	}
	return attrs
}

// attrsMap converts attributes to JSON values, resolving them and dropping empty
// attributes and groups as slog's handlers do.
func attrsMap(attrs []slog.Attr) map[string]any {
	values := map[string]any{}
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		switch {
		case attr.Equal(slog.Attr{}):
		case attr.Value.Kind() != slog.KindGroup:
			values[attr.Key] = attrValue(attr.Value)
		case attr.Key == "":
			maps.Copy(values, attrsMap(attr.Value.Group()))
		default:
			if group := attrsMap(attr.Value.Group()); len(group) > 0 {
				values[attr.Key] = group
			}
		}
	}
	return values
}

// attrValue converts a resolved, non-group value the way slog's JSON handler does.
func attrValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindDuration:
		return int64(value.Duration())
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			if _, marshals := err.(json.Marshaler); !marshals {
				return err.Error()
			}
		}
	case slog.KindBool, slog.KindFloat64, slog.KindInt64, slog.KindString, slog.KindUint64, slog.KindGroup, slog.KindLogValuer:
	}
	return value.Any()
}

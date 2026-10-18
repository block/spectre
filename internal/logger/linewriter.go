package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
)

// LineWriter logs each line written to it as one record, so a child process's
// output joins the structured log stream. JSON object lines pass through structured.
type LineWriter struct {
	ctx     context.Context
	log     *slog.Logger
	level   slog.Level
	labels  []slog.Attr
	pending []byte
}

// NewLineWriter logs plain lines at level and attaches labels to every record,
// overriding same-named JSON members. Give each stream its own writer; it is not concurrency safe.
func NewLineWriter(ctx context.Context, log *slog.Logger, level slog.Level, labels ...slog.Attr) *LineWriter {
	return &LineWriter{ctx: ctx, log: log.With(attrsToAny(labels)...), level: level, labels: labels}
}

// Write buffers data and logs every complete line it contains.
func (w *LineWriter) Write(data []byte) (n int, err error) {
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return len(data), nil
		}
		w.emit(w.pending[:end])
		w.pending = w.pending[end+1:]
	}
}

// Flush logs any trailing output that was not terminated by a newline. Call it
// once the writer's source has closed.
func (w *LineWriter) Flush() {
	if len(w.pending) > 0 {
		w.emit(w.pending)
		w.pending = nil
	}
}

func (w *LineWriter) emit(line []byte) {
	text := strings.TrimRight(string(line), "\r")
	if text == "" {
		return
	}
	if fields, ok := parseObject(text); ok {
		w.emitStructured(fields)
		return
	}
	w.log.Log(w.ctx, w.level, text)
}

// emitStructured logs a parsed JSON line, lifting its message and level into the
// record and dropping its timestamp in favour of this process's own.
func (w *LineWriter) emitStructured(fields map[string]any) {
	message := takeString(fields, slog.MessageKey, "message")
	level := parseLevel(takeString(fields, slog.LevelKey, "severity"), w.level)
	takeString(fields, slog.TimeKey, "timestamp", "ts")
	for _, label := range w.labels {
		delete(fields, label.Key)
	}
	attrs := make([]slog.Attr, 0, len(fields))
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		attrs = append(attrs, slog.Any(key, fields[key]))
	}
	w.log.LogAttrs(w.ctx, level, message, attrs...)
}

// parseObject decodes a line that is a JSON object. Numbers stay as json.Number so
// large integers are re-encoded exactly.
func parseObject(line string) (fields map[string]any, ok bool) {
	if !strings.HasPrefix(strings.TrimLeft(line, " \t"), "{") {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || decoder.More() {
		return nil, false
	}
	return fields, true
}

// takeString removes the first of keys present in fields and returns it when it
// is a string. Non-string values are left in place as ordinary attributes.
func takeString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if text, isString := value.(string); isString {
			delete(fields, key)
			return text
		}
		return ""
	}
	return ""
}

// parseLevel maps a service's level name onto a slog level, accepting the common
// synonyms slog does not know. Unrecognised names use fallback.
func parseLevel(name string, fallback slog.Level) slog.Level {
	switch strings.ToLower(name) {
	case "":
		return fallback
	case "trace":
		return slog.LevelDebug
	case "warning":
		return slog.LevelWarn
	case "fatal", "panic", "critical":
		return slog.LevelError
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return fallback
	}
	return level
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for i, attr := range attrs {
		values[i] = attr
	}
	return values
}

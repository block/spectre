package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// LineConfig describes how a child process's JSON log lines map onto slog's
// names, so SPECTRE adapts to the process rather than guessing.
type LineConfig struct {
	// Fields renames a member to slog's name before msg, level and time are lifted.
	Fields map[string]string `name:"field" placeholder:"OLD=NEW" help:"Rename a JSON log field the process writes to slog's name before it is logged, e.g. message=msg, severity=level or timestamp=time. Repeat for each field."`
	// Levels maps the process's level names onto slog levels.
	Levels map[string]slog.Level `name:"level" placeholder:"NAME=LEVEL" help:"Map a level name the process writes onto debug, info, warn or error, e.g. warning=warn or fatal=error. slog's own names are always recognised. Repeat for each name."`
}

// LineWriter logs each line written to it as one record, so a child process's
// output joins the structured log stream. JSON object lines pass through structured.
type LineWriter struct {
	ctx     context.Context
	log     *slog.Logger
	level   slog.Level
	config  LineConfig
	labels  []slog.Attr
	pending []byte
}

// NewLineWriter logs plain lines at level and interprets JSON lines with config.
// labels are attached to every record and override same-named members.
func NewLineWriter(ctx context.Context, log *slog.Logger, level slog.Level, config LineConfig, labels ...slog.Attr) *LineWriter {
	return &LineWriter{ctx: ctx, log: log.With(attrsToAny(labels)...), level: level, config: config, labels: labels}
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
	for old, name := range w.config.Fields {
		if value, ok := fields[old]; ok {
			delete(fields, old)
			fields[name] = value
		}
	}
	message := takeString(fields, slog.MessageKey)
	level := w.level
	if name, ok := fields[slog.LevelKey].(string); ok {
		if parsed, known := w.parseLevel(name); known {
			level = parsed
			delete(fields, slog.LevelKey)
		}
	}
	// Dropped whatever its type, so JSON output never carries two time keys.
	delete(fields, slog.TimeKey)
	for _, label := range w.labels {
		delete(fields, label.Key)
	}
	attrs := make([]slog.Attr, 0, len(fields))
	for key, value := range fields {
		attrs = append(attrs, slog.Any(key, value))
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

// takeString removes key from fields and returns its value when it is a string.
// A non-string value is left in place as an ordinary attribute.
func takeString(fields map[string]any, key string) string {
	text, ok := fields[key].(string)
	if ok {
		delete(fields, key)
	}
	return text
}

// parseLevel maps the process's level name onto a slog level using the configured
// names, then slog's own. Both match case-insensitively.
func (w *LineWriter) parseLevel(name string) (level slog.Level, known bool) {
	for configured, mapped := range w.config.Levels {
		if strings.EqualFold(configured, name) {
			return mapped, true
		}
	}
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, false
	}
	return level, true
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for i, attr := range attrs {
		values[i] = attr
	}
	return values
}

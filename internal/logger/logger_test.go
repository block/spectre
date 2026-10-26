package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/logger"
)

func TestKongConfig(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		level slog.Level
		json  string
	}{
		{name: "Default", level: slog.LevelInfo},
		{name: "Debug", args: []string{"--log-level=debug"}, level: slog.LevelDebug},
		{name: "JSON", args: []string{"--log-json=DEFAULT"}, level: slog.LevelInfo, json: "DEFAULT"},
		{name: "Warn", args: []string{"--log-level=warn"}, level: slog.LevelWarn},
		{name: "Error", args: []string{"--log-level=error"}, level: slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cli struct {
				Log logger.Config `embed:"" prefix:"log-"`
			}
			cli.Log = logger.NewConfig()
			parser, err := kong.New(&cli)
			assert.NoError(t, err)
			_, err = parser.Parse(test.args)
			assert.NoError(t, err)
			expected := logger.NewConfig()
			expected.Level = test.level
			if test.json != "" {
				expected.JSON = parseTemplate(t, test.json)
			}
			assert.Equal(t, expected, cli.Log)
		})
	}
}

func TestDefaultJSON(t *testing.T) {
	var output bytes.Buffer
	config := logger.NewConfig()
	config.JSON = parseTemplate(t, "DEFAULT")
	logger.New(config, &output).InfoContext(t.Context(), "hi")
	var record map[string]any
	assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
	_, hasTime := record["time"].(string)
	assert.True(t, hasTime)
	delete(record, "time")
	assert.Equal(t, map[string]any{"level": "INFO", "msg": "hi"}, record)
}

// parseTemplate builds a template without Kong, as non-flag callers do.
func parseTemplate(t *testing.T, text string) logger.JSONTemplate {
	t.Helper()
	template, err := logger.ParseJSONTemplate(text)
	assert.NoError(t, err)
	return template
}

func TestKongRejectsInvalidJSON(t *testing.T) {
	var cli struct {
		Log logger.Config `embed:"" prefix:"log-"`
	}
	parser, err := kong.New(&cli)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{`--log-json={"message": "<message>"}`})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "--log-json")
}

func TestKongRejectsInvalidLevel(t *testing.T) {
	var cli struct {
		Log logger.Config `embed:"" prefix:"log-"`
	}
	cli.Log = logger.NewConfig()
	parser, err := kong.New(&cli)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"--log-level=invalid"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "--log-level")
}

func TestOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		json bool
	}{
		{name: "Text"},
		{name: "JSON", json: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			config := logger.NewConfig()
			if test.json {
				config.JSON = parseTemplate(t, "DEFAULT")
			}
			log := logger.New(config, &output)
			log.DebugContext(t.Context(), "hidden")
			assert.Equal(t, "", output.String())
			log.InfoContext(t.Context(), "visible", "count", 3)
			if !test.json {
				assert.Equal(t, "\x1b[92mINF\x1b[0m visible \x1b[2mcount=\x1b[0m3\n", output.String())
				return
			}
			var record map[string]any
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			_, hasTime := record["time"].(string)
			assert.True(t, hasTime)
			delete(record, "time")
			assert.Equal(t, map[string]any{"level": "INFO", "msg": "visible", "count": float64(3)}, record)
		})
	}
}

func TestTemplateOutput(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		template string
		level    slog.Level
		message  string
		attrs    []slog.Attr
		expected string
	}{
		{name: "TopLevelAttrs", template: `{"ts": "<time:unixms>", "severity": "<level:debug=DEBUG,info=INFO,warn=WARNING,error=ERROR>", "message": "<msg>"}`,
			level: slog.LevelWarn, message: "careful", attrs: []slog.Attr{slog.Int("count", 3)},
			expected: `{"ts": 1767225600000, "severity": "WARNING", "message": "careful", "count": 3}`},
		{name: "NestedAndConstant", template: `{"log": {"level": "<level>"}, "service": "payments", "tags": [1, "<msg>"], "context": "<attrs>"}`,
			level: slog.LevelWarn + 2, message: "hi", attrs: []slog.Attr{slog.String("a", "x")},
			expected: `{"log": {"level": "WARN+2"}, "service": "payments", "tags": [1, "hi"], "context": {"a": "x"}}`},
		{name: "TemplateKeyWins", template: `{"msg": "<msg>"}`, message: "real", attrs: []slog.Attr{slog.String("msg", "attr")},
			expected: `{"msg": "real"}`},
		{name: "LevelBetweenNames", template: `{"level": "<level:info=INFO,error=ERROR,error+4=FATAL>"}`,
			level: slog.LevelError + 2, expected: `{"level": "ERROR"}`},
		{name: "LevelBelowNames", template: `{"level": "<level:info=INFO,error=ERROR>"}`,
			level: slog.LevelDebug, expected: `{"level": "INFO"}`},
		{name: "NumericLevel", template: `{"level": "<level:debug-4=10,debug=20,info=30,warn=40,error=50,error+4=60>"}`,
			level: slog.LevelError + 4, expected: `{"level": 60}`},
		{name: "DefaultTime", template: `{"time": "<time>"}`, expected: `{"time": "2026-01-01T00:00:00Z"}`},
		{name: "LayoutTime", template: `{"time": "<time:2006-01-02>"}`, expected: `{"time": "2026-01-01"}`},
		{name: "ValueKinds", template: `{"context": "<attrs>"}`,
			attrs: []slog.Attr{
				slog.Any("error", errors.New("boom")), slog.Duration("elapsed", time.Second), slog.Bool("ok", true),
				slog.Group("empty"), slog.Group("http", slog.String("method", "GET")), slog.Group("", slog.Int("inlined", 1)),
			},
			expected: `{"context": {"error": "boom", "elapsed": 1000000000, "ok": true, "http": {"method": "GET"}, "inlined": 1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			config := logger.NewConfig()
			config.JSON = parseTemplate(t, test.template)
			config.Level = slog.LevelDebug - 8
			record := slog.NewRecord(at, test.level, test.message, 0)
			record.AddAttrs(test.attrs...)
			assert.NoError(t, logger.New(config, &output).Handler().Handle(t.Context(), record))
			assertJSON(t, test.expected, output.String())
		})
	}
}

func TestTemplateNoHTMLEscaping(t *testing.T) {
	var output bytes.Buffer
	config := logger.NewConfig()
	config.JSON = parseTemplate(t, `{"msg": "<msg>"}`)
	logger.New(config, &output).InfoContext(t.Context(), "<b>&")
	assert.Equal(t, `{"msg":"<b>&"}`+"\n", output.String())
}

func TestTemplateLogger(t *testing.T) {
	var output bytes.Buffer
	config := logger.NewConfig()
	config.JSON = parseTemplate(t, `{"msg": "<msg>"}`)
	config.Level = slog.LevelInfo
	log := logger.New(config, &output).With("request", "r1").WithGroup("http").With("method", "GET")
	log.DebugContext(t.Context(), "hidden")
	log.InfoContext(t.Context(), "visible", "status", 200)
	assertJSON(t, `{"msg": "visible", "request": "r1", "http": {"method": "GET", "status": 200}}`, output.String())
}

func TestTemplateRejected(t *testing.T) {
	for _, test := range []struct {
		name     string
		template string
		error    string
	}{
		{name: "NotObject", template: `["<msg>"]`, error: "must be a JSON object"},
		{name: "Empty", template: `{}`, error: "at least one field"},
		{name: "UnknownPlaceholder", template: `{"msg": "<message>"}`, error: "unknown log template placeholder <message>"},
		{name: "MessageArgument", template: `{"msg": "<msg:x>"}`, error: "<msg> takes no argument"},
		{name: "AttrsTwice", template: `{"a": "<attrs>", "b": {"c": "<attrs>"}}`, error: "<attrs> more than once"},
		{name: "LevelPair", template: `{"level": "<level:info>"}`, error: "must be LEVEL=NAME"},
		{name: "LevelName", template: `{"level": "<level:loud=LOUD>"}`, error: "log template level"},
		{name: "LevelTwice", template: `{"level": "<level:warn=WARN,warn=WARNING>"}`, error: "more than once"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := logger.ParseJSONTemplate(test.template)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.error)
		})
	}
}

// assertJSON compares one JSON line with expected by value, decoding numbers exactly.
func assertJSON(t *testing.T, expected string, actual string) {
	t.Helper()
	decode := func(text string) any {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		assert.NoError(t, decoder.Decode(&value))
		return value
	}
	assert.Equal(t, decode(expected), decode(actual))
}

func TestLevelFiltering(t *testing.T) {
	for _, test := range []struct {
		name  string
		level slog.Level
	}{
		{name: "Debug", level: slog.LevelDebug},
		{name: "Info", level: slog.LevelInfo},
		{name: "Warn", level: slog.LevelWarn},
		{name: "Error", level: slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			config := logger.NewConfig()
			config.Level = test.level
			log := logger.New(config, &output)
			log.Log(t.Context(), test.level-1, "hidden")
			assert.Equal(t, "", output.String())
			log.Log(t.Context(), test.level, "visible")
			assert.Contains(t, output.String(), "visible")
		})
	}
}

func TestContextLogger(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	log := logger.New(logger.NewConfig(), io.Discard)
	ctx := logger.WithLogger(parent, log)
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()
	// Context propagation must retain logger identity, including any attached attributes.
	assert.True(t, logger.FromContext(child) == log)
	other := log.With("request", "other")
	override := logger.WithLogger(ctx, other)
	assert.True(t, logger.FromContext(override) == other)
	assert.True(t, logger.FromContext(ctx) == log)
	assert.False(t, logger.FromContext(parent).Enabled(parent, slog.LevelError))
	cancel()
	assert.Error(t, child.Err())
}

func TestMissingContextLogger(t *testing.T) {
	ctx := t.Context()
	assert.False(t, logger.FromContext(ctx).Enabled(ctx, slog.LevelError))
	ctx = logger.WithLogger(ctx, nil)
	assert.False(t, logger.FromContext(ctx).Enabled(ctx, slog.LevelError))
}

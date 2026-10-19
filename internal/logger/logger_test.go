package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/logger"
)

func TestKongConfig(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		level slog.Level
		json  bool
		remap map[string]string
	}{
		{name: "Default", level: slog.LevelInfo},
		{name: "DebugJSON", args: []string{"--log-level=debug", "--log-json"}, level: slog.LevelDebug, json: true},
		{name: "Remap", args: []string{"--log-remap=msg=message", "--log-remap=time=timestamp"}, level: slog.LevelInfo, remap: map[string]string{"msg": "message", "time": "timestamp"}},
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
			expected.JSON = test.json
			expected.Remap = test.remap
			assert.Equal(t, expected, cli.Log)
		})
	}
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
			config.JSON = test.json
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

func TestRemap(t *testing.T) {
	var output bytes.Buffer
	config := logger.NewConfig()
	config.JSON = true
	config.Remap = map[string]string{"msg": "message", "time": "timestamp", "count": "n"}
	log := logger.New(config, &output)
	log.InfoContext(t.Context(), "visible", "count", 3, slog.Group("nested", "count", 4))
	var record map[string]any
	assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
	_, hasTime := record["timestamp"].(string)
	assert.True(t, hasTime)
	delete(record, "timestamp")
	// Only top-level keys are renamed; the nested count keeps its name.
	assert.Equal(t, map[string]any{"level": "INFO", "message": "visible", "n": float64(3), "nested": map[string]any{"count": float64(4)}}, record)
}

func TestLineWriter(t *testing.T) {
	var output bytes.Buffer
	writer := logger.NewLineWriter(t.Context(), timelessLogger(&output), slog.LevelWarn, logger.LineConfig{}, slog.String("subsystem", "candidate"))
	for _, chunk := range []string{"first ", "line\r\nsecond line\n\nthird", " line"} {
		n, err := writer.Write([]byte(chunk))
		assert.NoError(t, err)
		assert.Equal(t, len(chunk), n)
	}
	// The unterminated third line is held back until the writer is flushed.
	assert.Equal(t, `{"level":"WARN","msg":"first line","subsystem":"candidate"}
{"level":"WARN","msg":"second line","subsystem":"candidate"}
`, output.String())
	writer.Flush()
	assert.Equal(t, `{"level":"WARN","msg":"first line","subsystem":"candidate"}
{"level":"WARN","msg":"second line","subsystem":"candidate"}
{"level":"WARN","msg":"third line","subsystem":"candidate"}
`, output.String())
}

func TestLineWriterStructured(t *testing.T) {
	for _, test := range []struct {
		name     string
		config   logger.LineConfig
		line     string
		expected map[string]any
	}{
		{name: "Slog", line: `{"time":"2026-01-01T00:00:00Z","level":"DEBUG","msg":"hello","count":12345678901234567890,"nested":{"a":1}}`,
			expected: map[string]any{"level": "DEBUG", "msg": "hello", "count": json.Number("12345678901234567890"), "nested": map[string]any{"a": json.Number("1")}}},
		{name: "Renamed", config: logger.LineConfig{Fields: map[string]string{"message": "msg", "severity": "level", "timestamp": "time"}},
			line:     `{"timestamp":"2026-01-01T00:00:00Z","severity":"error","message":"careful"}`,
			expected: map[string]any{"level": "ERROR", "msg": "careful"}},
		// Without a rename, unfamiliar names are ordinary attributes rather than guesses.
		{name: "NotRenamed", line: `{"severity":"error","message":"careful"}`,
			expected: map[string]any{"level": "INFO", "msg": "", "message": "careful", "severity": "error"}},
		{name: "MappedLevel", config: logger.LineConfig{Levels: map[string]slog.Level{"fatal": slog.LevelError}},
			line:     `{"level":"FATAL","msg":"gone"}`,
			expected: map[string]any{"level": "ERROR", "msg": "gone"}},
		{name: "SlogLevel", line: `{"level":"warn","msg":"careful"}`,
			expected: map[string]any{"level": "WARN", "msg": "careful"}},
		{name: "NoMessage", line: `{"event":"started"}`,
			expected: map[string]any{"level": "INFO", "msg": "", "event": "started"}},
		{name: "LabelWins", line: `{"msg":"x","subsystem":"theirs"}`,
			expected: map[string]any{"level": "INFO", "msg": "x"}},
		{name: "Array", line: `["not","an","object"]`,
			expected: map[string]any{"level": "INFO", "msg": `["not","an","object"]`}},
		{name: "Malformed", line: `{"msg":"unterminated`,
			expected: map[string]any{"level": "INFO", "msg": `{"msg":"unterminated`}},
		{name: "Trailing", line: `{"msg":"a"} {"msg":"b"}`,
			expected: map[string]any{"level": "INFO", "msg": `{"msg":"a"} {"msg":"b"}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := logger.NewLineWriter(t.Context(), timelessLogger(&output), slog.LevelInfo, test.config, slog.String("subsystem", "candidate"))
			_, err := writer.Write([]byte(test.line + "\n"))
			assert.NoError(t, err)
			decoder := json.NewDecoder(&output)
			decoder.UseNumber()
			var record map[string]any
			assert.NoError(t, decoder.Decode(&record))
			test.expected["subsystem"] = "candidate"
			assert.Equal(t, test.expected, record)
		})
	}
}

// An unmapped level is kept as an attribute, which duplicates the level key in
// JSON output; check the raw line for both rather than decoding it.
func TestLineWriterUnknownLevel(t *testing.T) {
	for _, test := range []struct {
		name string
		line string
		kept string
	}{
		{name: "NonString", line: `{"level":30,"msg":"pino"}`, kept: `"level":30`},
		{name: "Unmapped", line: `{"level":"fatal","msg":"gone"}`, kept: `"level":"fatal"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := logger.NewLineWriter(t.Context(), timelessLogger(&output), slog.LevelInfo, logger.LineConfig{}, slog.String("subsystem", "candidate"))
			_, err := writer.Write([]byte(test.line + "\n"))
			assert.NoError(t, err)
			assert.Contains(t, output.String(), `"level":"INFO"`)
			assert.Contains(t, output.String(), test.kept)
		})
	}
}

func TestLineConfigFlags(t *testing.T) {
	var cli struct {
		Service logger.LineConfig `embed:"" prefix:"service-log-"`
	}
	parser, err := kong.New(&cli)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"--service-log-field=message=msg", "--service-log-level=warning=warn", "--service-log-level=fatal=error"})
	assert.NoError(t, err)
	assert.Equal(t, logger.LineConfig{
		Fields: map[string]string{"message": "msg"},
		Levels: map[string]slog.Level{"warning": slog.LevelWarn, "fatal": slog.LevelError},
	}, cli.Service)
	_, err = parser.Parse([]string{"--service-log-level=fatal=loud"})
	assert.Error(t, err)
}

// timelessLogger writes every level as JSON without the time key, so output is comparable.
func timelessLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: func(groups []string, attribute slog.Attr) slog.Attr {
		if len(groups) == 0 && attribute.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attribute
	}}))
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

func TestTaggedEvent(t *testing.T) {
	var output bytes.Buffer
	config := logger.NewConfig()
	config.JSON = true
	log := logger.Tagged(logger.New(config, &output))
	log.InfoContext(t.Context(), "visible", "event", logger.EventQuarantine)
	var record map[string]any
	assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, logger.Component, record["component"])
	assert.Equal(t, logger.EventQuarantine, record["event"])
}

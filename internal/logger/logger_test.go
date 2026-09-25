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
	}{
		{name: "Default", level: slog.LevelInfo},
		{name: "DebugJSON", args: []string{"--log-level=debug", "--log-json"}, level: slog.LevelDebug, json: true},
		{name: "Warn", args: []string{"--log-level=warn"}, level: slog.LevelWarn},
		{name: "Error", args: []string{"--log-level=error"}, level: slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cli struct {
				Log logger.Config `embed:""`
			}
			cli.Log = logger.NewConfig()
			parser, err := kong.New(&cli)
			assert.NoError(t, err)
			_, err = parser.Parse(test.args)
			assert.NoError(t, err)
			expected := logger.NewConfig()
			expected.Level = test.level
			expected.JSON = test.json
			assert.Equal(t, expected, cli.Log)
		})
	}
}

func TestKongRejectsInvalidLevel(t *testing.T) {
	config := logger.NewConfig()
	parser, err := kong.New(&config)
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

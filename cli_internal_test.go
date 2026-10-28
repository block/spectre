package spectre

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/cli"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/storage"
	"github.com/block/spectre/internal/supervisor"
)

// reportingFlags are the SPECTRE server flags every supervise command requires.
func reportingFlags() []string {
	return []string{"--server-url=http://server.example", "--service=users", "--environment=production", "--revision=abc123"}
}

// expectedReporting is the reporter config that reportingFlags parse to.
func expectedReporting() server.ReporterConfig {
	return server.ReporterConfig{
		Client:      client.Config{URL: "http://server.example"},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
	}
}

func TestSuperviseCommandEmbedsConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli.CommandLine{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse(append([]string{
		"supervise",
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--destination=forecasts.example=http://127.0.0.1:50053",
		"--match-window=2s",
		"--scripts-dir=" + scripts,
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
		"--no-limits",
	}, reportingFlags()...))
	assert.NoError(t, err)
	expected := supervisor.NewConfig()
	expected.Candidate = "run-candidate"
	expected.Reference = "run-reference"
	expected.Destinations = map[string]string{"forecasts.example": "http://127.0.0.1:50053"}
	expected.MatchWindow = 2 * time.Second
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	expected.Descriptors.DescriptorsDir = descriptors
	expected.Limits.Enabled = false
	expected.Server = expectedReporting()
	assert.Equal(t, expected, command.Supervise.Supervisor)
}

func TestSuperviseCommandReadsFlagsFromEnvironment(t *testing.T) {
	t.Setenv("SPECTRE_SERVER_URL", "http://server.example")
	t.Setenv("SPECTRE_SERVICE", "invoices")
	t.Setenv("SPECTRE_ENVIRONMENT", "production-us-west-2")
	t.Setenv("SPECTRE_REVISION", "3f9c2a1")
	command := &cli.CommandLine{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"supervise",
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})
	assert.NoError(t, err)
	expected := server.ReporterConfig{
		Client:      client.Config{URL: "http://server.example"},
		Service:     "invoices",
		Environment: "production-us-west-2",
		Revision:    "3f9c2a1",
	}
	assert.Equal(t, expected, command.Supervise.Supervisor.Server)
}

func TestVersionFlagIgnoresEnvironment(t *testing.T) {
	parser, err := newParser(&cli.CommandLine{})
	assert.NoError(t, err)
	for _, flag := range parser.Model.Flags {
		if flag.Name == "version" {
			assert.Equal(t, 0, len(flag.Envs))
			return
		}
	}
	t.Fatal("no version flag")
}

func TestDivergencesCommands(t *testing.T) {
	command := &cli.CommandLine{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"divergences", "--server-url=http://server.example", "--service=users"})
	assert.NoError(t, err)
	assert.Equal(t, "users", command.Divergences.List.Service)

	command = &cli.CommandLine{}
	parser, err = newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"divergences", "clear", "--server-url=http://server.example", "--service=users", "--environment=production"})
	assert.NoError(t, err)
	expected := server.ClearConfig{Client: client.Config{URL: "http://server.example"}, Service: "users", Environment: "production"}
	assert.Equal(t, expected, command.Divergences.Clear.Clear)
}

func TestServeCommandEmbedsConfig(t *testing.T) {
	command := &cli.CommandLine{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"serve", "--listen=127.0.0.1:50071", "--storage=redis", "--redis-url=redis://localhost:6379/0"})
	assert.NoError(t, err)
	expected := server.ServeConfig{
		Listen:  "127.0.0.1:50071",
		Storage: storage.Config{Storage: storage.BackendRedis, RedisURL: "redis://localhost:6379/0"},
	}
	assert.Equal(t, expected, command.Serve.Serve)
}

func TestSuperviseCommandRequiresFlags(t *testing.T) {
	complete := append([]string{"--candidate=run-candidate", "--reference=run-reference"}, reportingFlags()...)
	tests := []struct {
		name    string
		missing string
	}{
		{name: "Candidate", missing: "--candidate="},
		{name: "Reference", missing: "--reference="},
		{name: "ServerURL", missing: "--server-url="},
		{name: "Service", missing: "--service="},
		{name: "Environment", missing: "--environment="},
		{name: "Revision", missing: "--revision="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser, err := newParser(&cli.CommandLine{})
			assert.NoError(t, err)
			args := slices.DeleteFunc(slices.Clone(complete), func(arg string) bool { return strings.HasPrefix(arg, test.missing) })
			args = append([]string{"supervise", "--scripts-dir=" + t.TempDir(), "--schema-dir=" + writeSchemaDir(t)}, args...)

			_, err = parser.Parse(args)

			assert.Error(t, err)
		})
	}
}

func TestSuperviseCommandLoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
candidate = "run-candidate"
reference = "run-reference"
match-window = "2s"
scripts-dir = %q
schema-dir = %q
descriptors-dir = %q

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
`, scripts, schema, descriptors)), 0o600)
	assert.NoError(t, err)
	command := &cli.CommandLine{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse(append([]string{"supervise", "--config=" + path, "--match-window=3s"}, reportingFlags()...))
	assert.NoError(t, err)
	expected := supervisor.NewConfig()
	expected.Candidate = "run-candidate"
	expected.Reference = "run-reference"
	expected.Destinations = map[string]string{"forecasts.example": "http://127.0.0.1:50053"}
	expected.MatchWindow = 3 * time.Second
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	expected.Descriptors.DescriptorsDir = descriptors
	expected.Server = expectedReporting()
	assert.Equal(t, expected, command.Supervise.Supervisor)
}

func TestSuperviseCommandRejectsUnknownConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectre.toml")
	assert.NoError(t, os.WriteFile(path, []byte(`canddate = "run-candidate"`), 0o600))
	parser, err := newParser(&cli.CommandLine{})
	assert.NoError(t, err)

	_, err = parser.Parse(append([]string{
		"supervise",
		"--config=" + path,
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	}, reportingFlags()...))

	assert.EqualError(t, err, path+": unknown configuration keys: canddate")
}

func TestProxySubcommandsRemoved(t *testing.T) {
	for _, name := range []string{"__ingress", "__egress"} {
		parser, err := newParser(&cli.CommandLine{})
		assert.NoError(t, err)
		_, err = parser.Parse([]string{name})
		assert.Error(t, err)
	}
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "payload.d.ts"), []byte(`declare module "payload" { interface Payload { value: string; } }`), 0o600))
	return dir
}

package spectre

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/cli"
	"github.com/block/spectre/internal/contain"
	httpprotocol "github.com/block/spectre/internal/protocols/http"
	mysqlprotocol "github.com/block/spectre/internal/protocols/mysql"
	statsdprotocol "github.com/block/spectre/internal/protocols/statsd"
	"github.com/block/spectre/internal/proxies"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/server/storage"
)

// reportingFlags are the SPECTRE server flags every proxy command requires.
func reportingFlags() []string {
	return []string{"--server-url=http://server.example", "--service=users", "--environment=production", "--revision=abc123"}
}

// expectedReporting is the reporter config that reportingFlags parse to.
func expectedReporting() reporter.Config {
	return reporter.Config{
		Client:            client.Config{URL: "http://server.example"},
		Service:           "users",
		Environment:       "production",
		Revision:          "abc123",
		KeepaliveInterval: 30 * time.Second,
		StreamMaxAge:      10 * time.Minute,
	}
}

// serviceFlags are the service addresses every proxy command requires.
func serviceFlags() []string {
	return []string{"--reference-url=h2c+unix:/sockets/reference.sock", "--candidate-url=h2c+unix:/sockets/candidate.sock"}
}

// newCommandLine returns the built-in command line, as Main builds it.
func newCommandLine(t *testing.T) *cli.CommandLine {
	t.Helper()
	registry, err := builtinRegistry()
	assert.NoError(t, err)
	return cli.NewCommandLine(registry.Plugins())
}

// parsedProviderConfig returns the registered config of type C, as Kong parsed it. The plugins
// hide their type, so it reads the "Config" field that each embeds for Kong.
func parsedProviderConfig[C any](t *testing.T, command *cli.CommandLine) C {
	t.Helper()
	for _, plugin := range command.Proxy.Providers {
		if config, ok := reflect.ValueOf(plugin).Elem().FieldByName("Config").Interface().(C); ok {
			return config
		}
	}
	var missing C
	t.Fatalf("no registered config of type %T", missing)
	return missing
}

// expectedMirror is the shared proxy config that reportingFlags parse to.
func expectedMirror() proxies.Config {
	expected := proxies.NewConfig()
	expected.EgressHealthListen = "127.0.0.1:50062"
	expected.HealthListen = "127.0.0.1:50063"
	expected.Server = expectedReporting()
	return expected
}

// expectedHTTP is the HTTP config that serviceFlags parse to.
func expectedHTTP() httpprotocol.Config {
	expected := httpprotocol.NewConfig()
	expected.ReferenceURL = "h2c+unix:/sockets/reference.sock"
	expected.CandidateURL = "h2c+unix:/sockets/candidate.sock"
	return expected
}

func TestProxyCommandReadsFlagsFromEnvironment(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	for name, value := range map[string]string{
		"SPECTRE_LISTEN":                       "0.0.0.0:8080",
		"SPECTRE_REFERENCE_URL":                "h2c+unix:/sockets/reference.sock",
		"SPECTRE_CANDIDATE_URL":                "h2c+unix:/sockets/candidate.sock",
		"SPECTRE_REFERENCE_EGRESS_LISTEN":      "unix:/sockets/egress-reference.sock",
		"SPECTRE_CANDIDATE_EGRESS_LISTEN":      "unix:/sockets/egress-candidate.sock",
		"SPECTRE_HEALTH_LISTEN":                "0.0.0.0:8081",
		"SPECTRE_CANDIDATE_STARTUP_TIMEOUT":    "10m",
		"SPECTRE_SCRIPTS_DIR":                  scripts,
		"SPECTRE_SCHEMA_DIR":                   schema,
		"SPECTRE_SERVER_URL":                   "http://server.example",
		"SPECTRE_SERVICE":                      "invoices",
		"SPECTRE_ENVIRONMENT":                  "production-us-west-2",
		"SPECTRE_REVISION":                     "3f9c2a1",
		"SPECTRE_MYSQL_UPSTREAM":               "db.example:3306",
		"SPECTRE_MYSQL_UPSTREAM_USER":          "spectre",
		"SPECTRE_MYSQL_UPSTREAM_PASSWORD_FILE": "/secrets/password",
		"SPECTRE_MYSQL_CANDIDATE_LISTEN":       "unix:/sockets/mysql-candidate.sock",
		"SPECTRE_STATSD_REFERENCE_LISTEN":      "unix:@statsd-reference.sock",
		"SPECTRE_STATSD_CANDIDATE_LISTEN":      "unix:@statsd-candidate.sock",
		"SPECTRE_STATSD_FORWARD":               "unix:@veneur.sock",
	} {
		t.Setenv(name, value)
	}
	command := newCommandLine(t)
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"proxy"})
	assert.NoError(t, err)
	expectedHTTPConfig := expectedHTTP()
	expectedHTTPConfig.Listen = "0.0.0.0:8080"
	expectedHTTPConfig.ReferenceEgressListen = "unix:/sockets/egress-reference.sock"
	expectedHTTPConfig.CandidateEgressListen = "unix:/sockets/egress-candidate.sock"
	expectedHTTPConfig.CandidateStartupTimeout = 10 * time.Minute
	expectedMySQL := mysqlprotocol.NewConfig()
	expectedMySQL.Upstream = "db.example:3306"
	expectedMySQL.UpstreamUser = "spectre"
	expectedMySQL.UpstreamPasswordFile = "/secrets/password"
	expectedMySQL.CandidateListen = "unix:/sockets/mysql-candidate.sock"
	expected := expectedMirror()
	expected.HealthListen = "0.0.0.0:8081"
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	expected.Server.Client.URL = "http://server.example"
	expected.Server.Service = "invoices"
	expected.Server.Environment = "production-us-west-2"
	expected.Server.Revision = "3f9c2a1"
	assert.Equal(t, expected, command.Proxy.Proxy)
	assert.Equal(t, expectedHTTPConfig, parsedProviderConfig[httpprotocol.Config](t, command))
	assert.Equal(t, expectedMySQL, parsedProviderConfig[mysqlprotocol.Config](t, command))
	expectedStatsd := statsdprotocol.NewConfig()
	expectedStatsd.ReferenceListen = "unix:@statsd-reference.sock"
	expectedStatsd.CandidateListen = "unix:@statsd-candidate.sock"
	expectedStatsd.Forward = "unix:@veneur.sock"
	assert.Equal(t, expectedStatsd, parsedProviderConfig[statsdprotocol.Config](t, command))
}

func TestContainCommandReadsCandidateUIDFromEnvironment(t *testing.T) {
	t.Setenv("SPECTRE_CANDIDATE_UID", "1002")
	command := newCommandLine(t)
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"contain"})
	assert.NoError(t, err)
	assert.Equal(t, contain.Config{CandidateUID: 1002}, command.Contain.Contain)
}

func TestVersionFlagIgnoresEnvironment(t *testing.T) {
	parser, err := newParser(newCommandLine(t))
	assert.NoError(t, err)
	for _, flag := range parser.Model.Flags {
		if flag.Name == "version" {
			assert.Equal(t, 0, len(flag.Envs))
			return
		}
	}
	t.Fatal("no version flag")
}

func TestStatusCommands(t *testing.T) {
	command := newCommandLine(t)
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"status", "--server-url=http://server.example", "--service=users", "--revision=abc123"})
	assert.NoError(t, err)
	assert.Equal(t, "users", command.Status.List.Service)
	assert.Equal(t, "abc123", command.Status.List.Revision)
	assert.Equal(t, client.ServersConfig{URLs: []string{"http://server.example"}}, command.Status.Servers)

	// The server URL belongs to the group, so it applies to clear wherever it
	// appears, not to the default list command.
	for _, args := range [][]string{
		{"status", "clear", "--server-url=http://server.example", "--service=users", "--environment=production"},
		{"status", "--server-url=http://server.example", "clear", "--service=users", "--environment=production"},
	} {
		command = newCommandLine(t)
		parser, err = newParser(command)
		assert.NoError(t, err)
		_, err = parser.Parse(args)
		assert.NoError(t, err)
		assert.Equal(t, client.ServersConfig{URLs: []string{"http://server.example"}}, command.Status.Servers)
		assert.Equal(t, server.ClearConfig{Service: "users", Environment: "production"}, command.Status.Clear.Clear)
	}
}

func TestServeCommandEmbedsConfig(t *testing.T) {
	command := newCommandLine(t)
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"serve", "--listen=127.0.0.1:50071", "--storage=redis://localhost:6379/0"})
	assert.NoError(t, err)
	expected := server.ServeConfig{
		Listen:   "127.0.0.1:50071",
		Storage:  storage.Config{Storage: &url.URL{Scheme: "redis", Host: "localhost:6379", Path: "/0"}},
		StateTTL: 90 * time.Second,
		CallsTTL: 7 * 24 * time.Hour,
	}
	assert.Equal(t, expected, command.Serve.Serve)
}

func TestProxyCommandRequiresFlags(t *testing.T) {
	complete := append(serviceFlags(), reportingFlags()...)
	tests := []struct {
		name    string
		missing string
	}{
		{name: "ReferenceURL", missing: "--reference-url="},
		{name: "CandidateURL", missing: "--candidate-url="},
		{name: "ServerURL", missing: "--server-url="},
		{name: "Service", missing: "--service="},
		{name: "Environment", missing: "--environment="},
		{name: "Revision", missing: "--revision="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser, err := newParser(newCommandLine(t))
			assert.NoError(t, err)
			args := slices.DeleteFunc(slices.Clone(complete), func(arg string) bool { return strings.HasPrefix(arg, test.missing) })
			args = append([]string{"proxy", "--scripts-dir=" + t.TempDir(), "--schema-dir=" + writeSchemaDir(t)}, args...)

			_, err = parser.Parse(args)

			assert.Error(t, err)
		})
	}
}

func TestProxyCommandLoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
reference-url = "h2c+unix:/sockets/reference.sock"
candidate-url = "h2c+unix:/sockets/candidate.sock"
match-window = "2s"
scripts-dir = %q
schema-dir = %q
descriptors-dir = %q
mysql-upstream = "db.example:3306"
mysql-upstream-user = "spectre"
mysql-upstream-password-file = "/secrets/password"

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
`, scripts, schema, descriptors)), 0o600)
	assert.NoError(t, err)
	command := newCommandLine(t)
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse(append([]string{"proxy", "--config=" + path, "--match-window=3s"}, reportingFlags()...))
	assert.NoError(t, err)
	expectedHTTPConfig := expectedHTTP()
	expectedHTTPConfig.Destinations = map[string]string{"forecasts.example": "http://127.0.0.1:50053"}
	expectedHTTPConfig.Descriptors.DescriptorsDir = descriptors
	expectedMySQL := mysqlprotocol.NewConfig()
	expectedMySQL.Upstream = "db.example:3306"
	expectedMySQL.UpstreamUser = "spectre"
	expectedMySQL.UpstreamPasswordFile = "/secrets/password"
	expected := expectedMirror()
	expected.MatchWindow = 3 * time.Second
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	assert.Equal(t, expected, command.Proxy.Proxy)
	assert.Equal(t, expectedHTTPConfig, parsedProviderConfig[httpprotocol.Config](t, command))
	assert.Equal(t, expectedMySQL, parsedProviderConfig[mysqlprotocol.Config](t, command))
}

func TestProxyCommandRejectsUnknownConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectre.toml")
	assert.NoError(t, os.WriteFile(path, []byte(`refrence-url = "h2c+unix:/sockets/reference.sock"`), 0o600))
	parser, err := newParser(newCommandLine(t))
	assert.NoError(t, err)

	_, err = parser.Parse(append(append([]string{
		"proxy",
		"--config=" + path,
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	}, serviceFlags()...), reportingFlags()...))

	assert.EqualError(t, err, path+": unknown configuration keys: refrence-url")
}

func TestProxySubcommandsRemoved(t *testing.T) {
	for _, name := range []string{"__ingress", "__egress"} {
		parser, err := newParser(newCommandLine(t))
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

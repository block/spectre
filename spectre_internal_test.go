package spectre

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/cli"
)

func TestWithCommandAddsCommand(t *testing.T) {
	extension := &imageCommand{}
	command := &cli.CommandLine{}
	parser, err := newParser(command, WithCommand("image", "Build an image.", extension))
	assert.NoError(t, err)

	kctx, err := parser.Parse([]string{"image", "--tag=spectre:dev"})
	assert.NoError(t, err)
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	log := slog.New(slog.DiscardHandler)
	kctx.Bind(log)
	assert.NoError(t, kctx.Run())

	assert.Equal(t, &imageCommand{Tag: "spectre:dev", log: log}, extension)
}

func TestWithCommandKeepsBuiltInCommands(t *testing.T) {
	parser, err := newParser(&cli.CommandLine{}, WithCommand("image", "Build an image.", &imageCommand{}))
	assert.NoError(t, err)

	kctx, err := parser.Parse([]string{"gen", "module", "--output=spectre.d.ts"})
	assert.NoError(t, err)

	assert.Equal(t, "gen module", kctx.Command())
}

func TestWithKongOptionsAddsVars(t *testing.T) {
	extension := &pushCommand{}
	parser, err := newParser(&cli.CommandLine{},
		WithCommand("push", "Push an image.", extension),
		WithKongOptions(kong.Vars{"registry": "registry.example.com"}),
	)
	assert.NoError(t, err)

	_, err = parser.Parse([]string{"push"})
	assert.NoError(t, err)

	assert.Equal(t, &pushCommand{Image: "registry.example.com/spectre:" + internal.Version}, extension)
}

func TestWithTemplateRepoSetsDefault(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	assert.NoError(t, os.CopyFS(filepath.Join(repo, "custom"), fstest.MapFS{
		"{{ .name }}.txt": {Data: []byte(`{{ range split .envs }}[{{ . }}]{{ end }}`)},
	}))
	parser, err := newParser(&cli.CommandLine{}, WithTemplateRepo(repo))
	assert.NoError(t, err)

	kctx, err := parser.Parse([]string{"init", dir, "--template=custom", "--var=name=svc", "--var=envs=staging, production"})
	assert.NoError(t, err)
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	assert.NoError(t, kctx.Run())

	content, err := os.ReadFile(filepath.Join(dir, "spectre/svc.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "[staging][production]", string(content))
}

func TestWithServerURLSetsDefault(t *testing.T) {
	tests := []struct {
		name     string
		options  []Option
		env      string
		args     []string
		expected []string
	}{
		{name: "Default", options: []Option{WithServerURL("http://default.example")}, args: []string{"status"}, expected: []string{"http://default.example"}},
		{name: "Flag", options: []Option{WithServerURL("http://default.example")}, args: []string{"status", "--server-url=http://flag.example"}, expected: []string{"http://flag.example"}},
		{
			name:     "StatusServers",
			options:  []Option{WithServerURL("http://default.example"), WithStatusServerURLs("http://staging.example", "http://production.example")},
			args:     []string{"status"},
			expected: []string{"http://staging.example", "http://production.example"},
		},
		{
			name:     "RepeatedFlag",
			options:  []Option{WithStatusServerURLs("http://staging.example", "http://production.example")},
			args:     []string{"status", "--server-url=http://a.example", "--server-url=http://b.example"},
			expected: []string{"http://a.example", "http://b.example"},
		},
		{
			name:     "Environment",
			options:  []Option{WithStatusServerURLs("http://staging.example")},
			env:      "http://a.example,http://b.example",
			args:     []string{"status"},
			expected: []string{"http://a.example", "http://b.example"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv("SPECTRE_SERVER_URL", test.env)
			}
			command := &cli.CommandLine{}
			parser, err := newParser(command, test.options...)
			assert.NoError(t, err)

			_, err = parser.Parse(test.args)
			assert.NoError(t, err)

			assert.Equal(t, test.expected, command.Status.Servers.URLs)
		})
	}
}

func TestWithStatusServerURLsLeavesSupervise(t *testing.T) {
	command := &cli.CommandLine{}
	parser, err := newParser(command, WithServerURL("http://default.example"), WithStatusServerURLs("http://staging.example", "http://production.example"))
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"supervise",
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
		"--service=users",
		"--environment=production",
		"--revision=abc123",
	})
	assert.NoError(t, err)

	assert.Equal(t, "http://default.example", command.Supervise.Supervisor.Mirror.Server.Client.URL)
}

func TestServerURLRequiredWithoutDefault(t *testing.T) {
	parser, err := newParser(&cli.CommandLine{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{"status"})

	assert.EqualError(t, err, "status: no SPECTRE server URL; pass --server-url, or build spectre with spectre.WithServerURL")
}

// pushCommand's default needs both a variable from WithKongOptions and the
// built-in version variable.
type pushCommand struct {
	Image string `help:"Image to push." default:"${registry}/spectre:${version}"`
}

func (c *pushCommand) Run(_ context.Context, _ *slog.Logger) error { return nil }

// imageCommand records the logger it ran with. Kong fails to call Run unless
// both arguments are bound.
type imageCommand struct {
	Tag string `help:"Image tag."`

	log *slog.Logger
}

func (c *imageCommand) Run(_ context.Context, log *slog.Logger) error {
	c.log = log
	return nil
}

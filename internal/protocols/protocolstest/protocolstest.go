// Package protocolstest configures registered providers in tests, as the command line does.
package protocolstest

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"
)

// Configure parses args into the configs of the providers that plugins came from, so the Kong
// tags supply the defaults. It ignores the environment, which a test must not depend on.
func Configure(t *testing.T, plugins kong.Plugins, args ...string) {
	t.Helper()
	var command struct {
		Providers kong.Plugins `embed:""`
	}
	command.Providers = plugins
	parser, err := kong.New(&command)
	assert.NoError(t, err)
	_, err = parser.Parse(args)
	assert.NoError(t, err)
}

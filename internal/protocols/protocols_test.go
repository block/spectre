package protocols_test

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/protocolstest"
	"github.com/block/spectre/internal/state"
)

// fakeConfig is a config with one flag, which must not be "invalid".
type fakeConfig struct {
	Address string `default:"127.0.0.1:1" help:"Address the fake protocol listens on."`
}

func (c fakeConfig) Validate() error {
	if c.Address == "invalid" {
		return errors.New("address is invalid")
	}
	return nil
}

// fakeProvider proxies the directions it is given, under a protocol name of its own.
type fakeProvider struct {
	protocol   model.Protocol
	directions []model.Direction
	// kindProtocol is the protocol its kinds name, when it is not the provider's own.
	kindProtocol model.Protocol
	// built receives the config of each server that New builds.
	built []fakeConfig
}

func newFakeProvider(protocol model.Protocol, directions ...model.Direction) *fakeProvider {
	return &fakeProvider{protocol: protocol, directions: directions}
}

func (p *fakeProvider) Protocol() model.Protocol { return p.protocol }

func (p *fakeProvider) Kinds() []state.Kind {
	protocol := p.protocol
	if p.kindProtocol != "" {
		protocol = p.kindProtocol
	}
	kinds := make([]state.Kind, 0, len(p.directions))
	for _, direction := range p.directions {
		kinds = append(kinds, state.Kind{Direction: direction, Protocol: protocol, Description: "Fake."})
	}
	return kinds
}

func (p *fakeProvider) Scripts() javascript.Binding { return javascripttest.Binding() }

func (p *fakeProvider) Listens(config fakeConfig) []protocols.Listen {
	return []protocols.Listen{{Name: "main", Address: config.Address, Transport: protocols.Stream}}
}

func (p *fakeProvider) New(_ context.Context, config fakeConfig, _ protocols.Runtime) (protocols.Server, error) {
	p.built = append(p.built, config)
	return fakeServer{}, nil
}

type fakeServer struct{}

func (fakeServer) Serve(context.Context, protocols.Listeners) error { return nil }

// registerAll registers each provider.
func registerAll(registry *protocols.Registry, providers ...*fakeProvider) error {
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return err
		}
	}
	return nil
}

func TestRegister(t *testing.T) {
	for name, test := range map[string]struct {
		providers []*fakeProvider
		expected  []string
		err       string
	}{
		"None": {expected: []string{"library"}},
		"Combined": {
			providers: []*fakeProvider{
				newFakeProvider("alpha", model.Ingress, model.Egress),
				newFakeProvider("beta", model.Egress),
			},
			expected: []string{"library", "ingress[alpha]", "egress[alpha]", "egress[beta]"},
		},
		"ProtocolRegisteredTwice": {
			providers: []*fakeProvider{
				newFakeProvider("alpha", model.Ingress),
				newFakeProvider("alpha", model.Egress),
			},
			err: "protocol alpha is registered more than once",
		},
		"KindOfAnotherProtocol": {
			providers: []*fakeProvider{{protocol: "alpha", directions: []model.Direction{model.Egress}, kindProtocol: "beta"}},
			err:       "provider of protocol alpha defines egress[beta], which has another protocol",
		},
		"DirectionDefinedTwice": {
			providers: []*fakeProvider{newFakeProvider("alpha", model.Egress, model.Egress)},
			err:       "egress[alpha] is defined more than once",
		},
	} {
		t.Run(name, func(t *testing.T) {
			registry := protocols.NewRegistry()
			err := registerAll(registry, test.providers...)
			if test.err != "" {
				assert.EqualError(t, err, test.err)
				return
			}
			assert.NoError(t, err)
			actual := []string{}
			for _, nodeType := range registry.Kinds().NodeTypes() {
				actual = append(actual, nodeType.Name)
			}
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestRegistryKeepsRegistrationOrder(t *testing.T) {
	registry := protocols.NewRegistry()
	assert.NoError(t, registerAll(registry, newFakeProvider("beta"), newFakeProvider("alpha")))

	assert.Equal(t, []model.Protocol{"beta", "alpha"}, registry.Protocols())
	assert.Equal(t, 2, len(registry.Plugins()))
	assert.Equal(t, 2, len(registry.Bindings()))
}

func TestValidateChecksTheParsedConfig(t *testing.T) {
	for name, test := range map[string]struct {
		address string
		err     string
	}{
		"Valid":   {address: "127.0.0.1:2"},
		"Invalid": {address: "invalid", err: "configure alpha: address is invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			registry := protocols.NewRegistry()
			assert.NoError(t, registry.Register(newFakeProvider("alpha")))
			// Kong validates while it parses, but leaves the parsed values in the config, which Validate checks again.
			parser, err := kong.New(&struct {
				Providers kong.Plugins `embed:""`
			}{registry.Plugins()})
			assert.NoError(t, err)
			_, _ = parser.Parse([]string{"--address=" + test.address})

			err = registry.Validate()

			if test.err != "" {
				assert.EqualError(t, err, test.err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Registered providers are Kong plugins: each config's flags join the command's, with
// defaults and environment variables, and Listens then reports what was parsed.
func TestPluginsParseEachProvidersConfig(t *testing.T) {
	for name, test := range map[string]struct {
		args     []string
		env      string
		expected string
	}{
		"Default": {expected: "127.0.0.1:1"},
		"Flag":    {args: []string{"--address=127.0.0.1:2"}, expected: "127.0.0.1:2"},
		"Env":     {env: "127.0.0.1:3", expected: "127.0.0.1:3"},
	} {
		t.Run(name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv("TEST_ADDRESS", test.env)
			}
			registry := protocols.NewRegistry()
			assert.NoError(t, registry.Register(newFakeProvider("alpha")))
			var command struct {
				Providers kong.Plugins `embed:""`
			}
			command.Providers = registry.Plugins()
			parser, err := kong.New(&command, kong.DefaultEnvars("TEST"))
			assert.NoError(t, err)

			_, err = parser.Parse(test.args)

			assert.NoError(t, err)
			assert.Equal(t, []protocols.Listen{{Name: "main", Address: test.expected, Transport: protocols.Stream}}, registry.Listens("alpha"))
		})
	}
}

func TestNewBuildsTheServerFromTheWholeParsedConfig(t *testing.T) {
	provider := newFakeProvider("alpha")
	registry := protocols.NewRegistry()
	assert.NoError(t, registry.Register(provider))
	protocolstest.Configure(t, registry.Plugins(), "--address=127.0.0.1:2")

	_, err := registry.New(t.Context(), "alpha", protocols.Runtime{})

	assert.NoError(t, err)
	assert.Equal(t, []fakeConfig{{Address: "127.0.0.1:2"}}, provider.built)
}

func TestNewFailsForAnUnregisteredProtocol(t *testing.T) {
	_, err := protocols.NewRegistry().New(t.Context(), "alpha", protocols.Runtime{})

	assert.EqualError(t, err, "protocol alpha is not registered")
}

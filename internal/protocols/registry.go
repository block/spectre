package protocols

import (
	"context"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

// Registry is the set of protocols a binary supports. It owns the checks across
// providers: no protocol is registered twice and no kind is defined twice.
type Registry struct {
	registered []registered
	kinds      state.Kinds
}

// NewRegistry returns a Registry that supports no protocols.
func NewRegistry() *Registry {
	return &Registry{kinds: state.NewKinds()}
}

// Register adds provider. Kong parses its flags into its config when the registry's plugins join a command.
func (r *Registry) Register[C Config](provider Provider[C]) error {
	entry := newRegistration(provider)
	protocol := entry.protocol()
	if _, duplicate := r.lookup(protocol); duplicate {
		return errors.Errorf("protocol %s is registered more than once", protocol)
	}
	for _, kind := range entry.kinds() {
		if kind.Protocol != protocol {
			return errors.Errorf("provider of protocol %s defines %s, which has another protocol", protocol, kind)
		}
		if err := r.kinds.Add(kind); err != nil {
			return errors.WithStack(err)
		}
	}
	r.registered = append(r.registered, entry)
	return nil
}

// Protocols returns the registered protocols in registration order.
func (r *Registry) Protocols() []model.Protocol {
	protocols := make([]model.Protocol, 0, len(r.registered))
	for _, entry := range r.registered {
		protocols = append(protocols, entry.protocol())
	}
	return protocols
}

// Listens returns the listeners that protocol's server serves, from its parsed config.
func (r *Registry) Listens(protocol model.Protocol) []Listen {
	entry, ok := r.lookup(protocol)
	if !ok {
		return nil
	}
	return entry.listens()
}

// New builds the server of protocol from its parsed config. An unregistered protocol is an error.
func (r *Registry) New(ctx context.Context, protocol model.Protocol, runtime Runtime) (Server, error) {
	entry, ok := r.lookup(protocol)
	if !ok {
		return nil, errors.Errorf("protocol %s is not registered", protocol)
	}
	server, err := entry.newServer(ctx, runtime)
	return server, errors.Wrapf(err, "configure %s", protocol)
}

// Kinds returns the kinds of IO that the providers proxy, for the migration state.
func (r *Registry) Kinds() state.Kinds {
	return r.kinds
}

// Bindings returns the part of the script module of each provider that has one, in registration order.
func (r *Registry) Bindings() []javascript.Binding {
	bindings := make([]javascript.Binding, 0, len(r.registered))
	for _, entry := range r.registered {
		if binding, ok := entry.scripts().Get(); ok {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

// Plugins returns the providers as Kong plugins, which parse their flags into their configs.
func (r *Registry) Plugins() kong.Plugins {
	plugins := make(kong.Plugins, 0, len(r.registered))
	for _, entry := range r.registered {
		plugins = append(plugins, entry)
	}
	return plugins
}

// Validate checks each provider's parsed config.
func (r *Registry) Validate() error {
	for _, entry := range r.registered {
		if err := entry.validate(); err != nil {
			return errors.Wrapf(err, "configure %s", entry.protocol())
		}
	}
	return nil
}

func (r *Registry) lookup(protocol model.Protocol) (entry registered, found bool) {
	for _, entry := range r.registered {
		if entry.protocol() == protocol {
			return entry, true
		}
	}
	return nil, false
}

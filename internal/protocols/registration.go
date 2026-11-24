package protocols

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

// registered is a provider with its config, whatever the config's type. It hides
// the type parameter, so a Registry can hold providers of different configs.
type registered interface {
	protocol() model.Protocol
	kinds() []state.Kind
	scripts() Option[javascript.Binding]
	validate() error
	listens() []Listen
	newServer(ctx context.Context, runtime Runtime) (Server, error)
}

// registration is a Kong plugin: Kong parses the provider's flags into Config,
// so call validate, listens and newServer only after it has parsed.
type registration[C Config] struct {
	Config   C `embed:""`
	provider Provider[C]
}

//nolint:configdefaults // Kong applies the config's defaults when it parses.
func newRegistration[C Config](provider Provider[C]) *registration[C] {
	return &registration[C]{provider: provider}
}

func (r *registration[C]) protocol() model.Protocol            { return r.provider.Protocol() }
func (r *registration[C]) kinds() []state.Kind                 { return r.provider.Kinds() }
func (r *registration[C]) scripts() Option[javascript.Binding] { return r.provider.Scripts() }
func (r *registration[C]) listens() []Listen                   { return r.provider.Listens(r.Config) }

func (r *registration[C]) validate() error {
	return errors.WithStack(r.Config.Validate())
}

func (r *registration[C]) newServer(ctx context.Context, runtime Runtime) (Server, error) {
	server, err := r.provider.New(ctx, r.Config, runtime)
	return server, errors.WithStack(err)
}

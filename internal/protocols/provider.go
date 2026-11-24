// Package protocols is the interface between SPECTRE and the protocols it mirrors.
//
// A provider holds everything specific to one protocol, and the registry holds the providers
// a binary supports. Only the root package imports the built-in providers' sub-packages.
package protocols

import (
	"context"
	"net"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

// Config is a provider's Kong-tagged configuration.
type Config interface {
	// Validate checks the parsed config.
	Validate() error
}

// Provider is everything SPECTRE knows about one protocol, configured by C.
// Kong parses C's flags and the registry passes the result to Listens and New.
type Provider[C Config] interface {
	// Protocol names the protocol in scripts, the migration state and quarantine reasons.
	Protocol() model.Protocol
	// Kinds describes each direction the protocol proxies and its migration-state settings.
	Kinds() []state.Kind
	// Scripts is the protocol's part of the spectre script module.
	Scripts() javascript.Binding
	// Listens names the listeners the server serves, with their addresses from config.
	Listens(config C) []Listen
	// New builds the protocol's proxies. An error fails startup before anything serves.
	New(ctx context.Context, config C, runtime Runtime) (Server, error)
}

// Listen is one listener a provider serves. The mirror opens them all before
// any provider starts, so a bad address fails startup.
type Listen struct {
	// Name is unique within the provider, such as "ingress" or "reference".
	Name    string
	Address string
}

// Server serves one provider's listeners.
type Server interface {
	// Serve blocks until ctx is done and the provider's in-flight work has ended.
	// The mirror stops candidate work before it cancels ctx.
	Serve(ctx context.Context, listeners map[string]net.Listener) error
}

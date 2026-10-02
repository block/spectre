package comparison

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/httpcodec"
	"github.com/block/spectre/internal/route"
	"github.com/block/spectre/internal/schema"
)

// configuredScripts keeps a plan and its wire decoder together, so a concurrent
// reconfiguration cannot mix declarations from one schema with another's codec.
type configuredScripts struct {
	plan  *comparisoninternal.Plan
	codec *httpcodec.Codec
}

// scriptSet shares an immutable program; only configuration replacement is synchronized.
type scriptSet struct {
	log       *slog.Logger
	program   *javascript.Program
	direction javascript.Direction
	routes    *route.Map[javascript.Endpoint]

	mu         sync.RWMutex
	configured *configuredScripts
}

func newScriptSet(ctx context.Context, config Config, direction javascript.Direction, log *slog.Logger) (*scriptSet, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		return nil, errors.New("logger is required")
	}
	root, err := os.OpenRoot(config.ScriptsDir)
	if err != nil {
		return nil, errors.Wrap(err, "open scripts directory")
	}
	defer root.Close() //nolint:errcheck // Module source is fully loaded before closing.
	program, err := javascript.NewProgram(ctx, config.ComparisonTimeout, os.DirFS(config.Schema.SchemaDir), root.FS())
	if err != nil {
		return nil, errors.Wrap(err, "compile comparison scripts")
	}
	routes := route.New[javascript.Endpoint]()
	for _, endpoint := range program.Endpoints(direction) {
		if endpoint.Protocol() != "http" {
			return nil, errors.Errorf("endpoint %q names unsupported protocol %q", endpoint.Pattern(), endpoint.Protocol())
		}
		if err := routes.Add(endpoint.Pattern(), endpoint); err != nil {
			return nil, errors.Wrapf(err, "route endpoint %q", endpoint.Pattern())
		}
	}
	return &scriptSet{log: log, program: program, direction: direction, routes: routes}, nil
}

func (s *scriptSet) endpoints() []javascript.Endpoint {
	return s.program.Endpoints(s.direction)
}

func (s *scriptSet) prepare(ctx context.Context, set *descriptorpb.FileDescriptorSet) (*configuredScripts, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, "configure comparison scripts")
	}
	loaded := s.program.Schema()
	for _, endpoint := range s.endpoints() {
		if _, err := loaded.Type(endpoint.Type()); err != nil {
			return nil, errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
	}
	codec, err := httpcodec.New(loaded, set)
	if err != nil {
		return nil, errors.Wrap(err, "configure wire decoding")
	}
	plan, err := comparisoninternal.NewPlan(loaded, s.program.Fields(), s.program.Messages())
	if err != nil {
		return nil, errors.Wrap(err, "prepare comparison plan")
	}
	return &configuredScripts{plan: plan, codec: codec}, nil
}

func (s *scriptSet) activate(configured *configuredScripts) {
	s.mu.Lock()
	s.configured = configured
	s.mu.Unlock()
}

func (s *scriptSet) active() *configuredScripts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configured
}

// resolve prefers explicit payload types, then falls back to RPC service paths.
func (s *scriptSet) resolve(configured *configuredScripts, method, host, path, contentType string,
) (root *schema.Type, identity string, selected protocol, wildcards map[string]string, result Result) {
	endpoint, wildcards, declared := s.routes.Match(method, host, path)
	selected, result = requestProtocol(contentType)
	if declared && selected != protocolGRPC && selected != protocolProtobuf {
		selected, result = protocolHTTPJSON, newEmptyResult()
	}
	if result.Outcome() != "" {
		return nil, "", 0, nil, result
	}
	loaded := configured.plan.Schema()
	name := endpoint.Type()
	identity = endpoint.Pattern()
	if !declared {
		operation, resolved := conventionalMethod(loaded, configured.codec, path)
		if resolved.Outcome() != "" {
			return nil, "", 0, nil, resolved
		}
		name, identity = operation.Response, operation.Name
		if s.direction == javascript.Egress {
			name = operation.Request
		}
	}
	root, err := loaded.Type(name)
	if err != nil {
		return nil, "", 0, nil, Resultf(Unable, "endpoint type is unusable: %v", err)
	}
	return root, identity, selected, wildcards, newEmptyResult()
}

// normalise gives each payload a fresh evaluator, so script state cannot carry
// between payloads and a payload's normalised form depends only on its content.
func (s *scriptSet) normalise(ctx context.Context, configured *configuredScripts, root *schema.Type,
	runNormalisers bool, side string, payload any,
) (*comparisoninternal.Normalised, error) {
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	normalised, err := configured.plan.Normalise(ctx, s.log, evaluator, root, runNormalisers, side, payload)
	return normalised, errors.Wrap(err, "normalise payload")
}

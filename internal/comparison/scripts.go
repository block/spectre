package comparison

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// Scripts is one immutable set of normaliser scripts and the schema they were
// checked against. It is safe for concurrent use.
type Scripts struct {
	log     *slog.Logger
	program *javascript.Program
	plan    *comparisoninternal.Plan
	timeout time.Duration
}

// New loads every script in the configured directory and checks that each
// declared endpoint type exists.
func New(ctx context.Context, config Config, log *slog.Logger) (*Scripts, error) {
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
	loaded := program.Schema()
	scripts := &Scripts{log: log, program: program, timeout: config.ComparisonTimeout}
	for _, endpoint := range scripts.Endpoints() {
		if endpoint.Ignore() {
			continue
		}
		if _, err := loaded.Type(endpoint.Type()); err != nil {
			return nil, errors.Wrapf(err, "resolve endpoint %q", endpoint.Pattern())
		}
	}
	scripts.plan, err = comparisoninternal.NewPlan(loaded, program.Fields(), program.Messages())
	if err != nil {
		return nil, errors.Wrap(err, "prepare comparison plan")
	}
	return scripts, nil
}

// Schema returns the payload types the scripts were checked against.
func (s *Scripts) Schema() *schema.Schema {
	return s.program.Schema()
}

// Endpoints returns every declared endpoint, ingress first, in declaration order.
func (s *Scripts) Endpoints() []javascript.Endpoint {
	return append(s.program.Endpoints(javascript.Ingress), s.program.Endpoints(javascript.Egress)...)
}

// Compare normalises both payloads as typeName and returns the paths at which
// they differ. Untyped payloads are compared without normalisers.
func (s *Scripts) Compare(ctx context.Context, typeName Option[schema.TypeRef], reference, candidate any) (differences []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalisedReference, err := s.normalise(ctx, typeName, "reference", reference)
	if err != nil {
		return nil, errors.Wrap(err, "normalise reference")
	}
	normalisedCandidate, err := s.normalise(ctx, typeName, "candidate", candidate)
	if err != nil {
		return nil, errors.Wrap(err, "normalise candidate")
	}
	return comparisoninternal.Diff(normalisedReference, normalisedCandidate), nil
}

// Hash returns SHA-256 over identity and the canonical JSON of the normalised
// payload. side only labels log records.
func (s *Scripts) Hash(ctx context.Context, identity string, typeName Option[schema.TypeRef], side string, payload any) (Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	normalised, err := s.normalise(ctx, typeName, side, payload)
	if err != nil {
		return Hash{}, errors.Wrap(err, "normalise payload")
	}
	canonical, err := normalised.CanonicalJSON()
	if err != nil {
		return Hash{}, errors.WithStack(err)
	}
	// Identities contain no zero bytes, separating identity from JSON.
	hashed := make([]byte, 0, len(identity)+1+len(canonical))
	hashed = append(hashed, identity...)
	hashed = append(hashed, 0)
	hashed = append(hashed, canonical...)
	return sha256.Sum256(hashed), nil
}

// normalise gives each payload a fresh evaluator, so script state cannot carry
// between payloads and a payload's normalised form depends only on its content.
func (s *Scripts) normalise(ctx context.Context, typeName Option[schema.TypeRef], side string, payload any) (*comparisoninternal.Normalised, error) {
	name, typed := typeName.Get()
	if !typed {
		return comparisoninternal.Raw(payload), nil
	}
	root, err := s.plan.Schema().Type(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	evaluator, err := s.program.NewEvaluator(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "initialise JavaScript evaluator")
	}
	defer evaluator.Close()
	return errors.WithStack2(s.plan.Normalise(ctx, s.log, evaluator, root, side, payload))
}

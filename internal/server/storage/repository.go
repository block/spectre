package storage

import (
	"context"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/block/spectre/internal/model"
)

// Repository applies domain transitions independently of the storage backend.
type Repository struct{ store Store }

// NewRepository uses store to persist each service environment as one aggregate.
func NewRepository(store Store) *Repository { return &Repository{store: store} }

// Observe replaces live state and optionally records its quarantines.
func (r *Repository) Observe(ctx context.Context, state model.State, at time.Time, recordQuarantines bool) error {
	err := r.store.Update(ctx, state.Service, state.Environment, func(e model.ServiceEnvironment) Option[model.ServiceEnvironment] {
		return Some(e.Observed(state, at, recordQuarantines))
	})
	return errors.Wrap(err, "observe service environment")
}

// List returns reporting states and recorded history, forgetting expired state.
func (r *Repository) List(ctx context.Context, since time.Time) (model.Status, error) {
	environments, err := r.store.List(ctx)
	if err != nil {
		return model.Status{}, errors.Wrap(err, "list service environments")
	}
	status := model.Status{States: []model.State{}, Divergences: []model.Divergence{}}
	for _, environment := range environments {
		if environment.State.Ok() && environment.Received.Before(since) {
			err := r.store.Update(ctx, environment.Service, environment.Environment, func(current model.ServiceEnvironment) Option[model.ServiceEnvironment] {
				return current.ForgetStateBefore(since).Retained()
			})
			if err != nil {
				return model.Status{}, errors.Wrap(err, "forget expired state")
			}
		}
		status = status.Plus(environment.ForgetStateBefore(since).Listed())
	}
	return status, nil
}

// Clear clears divergence history while retaining live state and the clear watermark.
func (r *Repository) Clear(ctx context.Context, service, environment string, at time.Time) error {
	err := r.store.Update(ctx, service, environment, func(e model.ServiceEnvironment) Option[model.ServiceEnvironment] {
		return Some(e.ClearedAt(at))
	})
	return errors.Wrap(err, "clear service environment")
}

// Close closes the backing store.
func (r *Repository) Close() error { return errors.Wrap(r.store.Close(), "close storage") }

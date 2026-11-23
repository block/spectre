package storage

import "example.com/architecture/internal/model"

func clearFromAdapter(e model.Environment) model.Environment {
 return e.Cleared() // want `storage adapters persist aggregates; apply domain transitions in the repository`
}

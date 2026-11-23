package storage

import "example.com/architecture/internal/model"

func clearFromRepository(e model.Environment) model.Environment {
 return e.Cleared()
}

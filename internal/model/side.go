package model

// Side is the service that sent traffic or a payload in a mirror.
type Side string

const (
	// Reference is the service being migrated from.
	Reference Side = "reference"
	// Candidate is the service being migrated to.
	Candidate Side = "candidate"
)

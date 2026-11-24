package proxy

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// CandidatesConfig contains the command-line configuration for candidate admission.
type CandidatesConfig struct {
	// CandidateMaxInFlight limits concurrent candidate requests.
	CandidateMaxInFlight int `default:"64" help:"Maximum concurrent candidate requests before quarantine."`
}

// NewCandidatesConfig returns the default candidate admission configuration.
func NewCandidatesConfig() CandidatesConfig {
	config := CandidatesConfig{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply candidate defaults"))
	}
	return config
}

// Validate checks that candidates can be admitted.
func (c CandidatesConfig) Validate() error {
	if c.CandidateMaxInFlight <= 0 {
		return errors.New("candidate maximum in-flight requests must be positive")
	}
	return nil
}

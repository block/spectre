// Package comparison compares paired unary RPC responses.
package comparison

import (
	"fmt"
	"net/http"
	"slices"
)

// Outcome describes whether a response pair could be compared and matched.
type Outcome string

const (
	// Equivalent means the supported response pair matched.
	Equivalent Outcome = "equivalent"
	// Divergent means the supported response pair did not match.
	Divergent Outcome = "divergent"
	// Skipped means the request is outside the supported protocol scope.
	Skipped Outcome = "skipped"
	// Unable means a supported request could not be compared safely.
	Unable Outcome = "unable"
)

// Response is the captured result from one backend.
type Response struct {
	// StatusCode is the backend's HTTP status before RPC-level status handling.
	StatusCode int
	// Header includes response trailers promoted by the HTTP transport.
	Header http.Header
	// Body contains at most the configured capture limit.
	Body []byte
	// Overflow reports that Body is truncated and must not be compared.
	Overflow bool
}

// Result is the value-safe result of comparing two responses.
type Result struct {
	outcome     Outcome
	differences []string
	reason      string
}

// Resultf constructs a result with a printf-formatted reason.
func Resultf(outcome Outcome, format string, args ...any) Result {
	return Result{outcome: outcome, reason: fmt.Sprintf(format, args...)}
}

// NewDifferenceResult constructs a divergent result without exposing response values.
func NewDifferenceResult(differences ...string) Result {
	return Result{outcome: Divergent, differences: slices.Clone(differences)}
}

func newEmptyResult() Result {
	return Result{}
}

// Outcome returns the comparison classification.
func (r Result) Outcome() Outcome {
	return r.outcome
}

// Differences returns a copy of the divergent JSON paths.
func (r Result) Differences() []string {
	return slices.Clone(r.differences)
}

// Reason returns why comparison was skipped or could not be completed.
func (r Result) Reason() string {
	return r.reason
}

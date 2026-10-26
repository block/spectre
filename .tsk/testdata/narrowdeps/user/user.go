package user

import (
	"context"
	"slices"

	"example.com/m/proxy"
)

// Source holds all of Candidates to read two of its values.
type Source struct {
	Name       string
	Candidates *proxy.Candidates // want `field Candidates holds Candidates only to read QuarantineStatus and Quarantined; depend on that instead`
}

func Report(source Source) proxy.Status {
	<-source.Candidates.Quarantined()
	return source.Candidates.QuarantineStatus()
}

func Wait(candidates *proxy.Candidates) { // want `parameter candidates holds Candidates only to read Quarantined; depend on that instead`
	<-candidates.Quarantined()
}

// Calling behaviour is a real dependency.
func Stop(ctx context.Context, candidates *proxy.Candidates) error {
	<-candidates.Quarantined()
	return candidates.Shutdown(ctx)
}

// A holder passed on whole is reported in its callee instead.
func Forward(candidates *proxy.Candidates) {
	Wait(candidates)
}

// A method returning only an error is behaviour, not a read.
func Drain(candidates *proxy.Candidates) error {
	return candidates.Flush()
}

// Getters do not make a type large.
func Unwrap(wrapped proxy.Wrapped) int {
	return wrapped.Value()
}

// A method value observes the receiver when it is called later.
func StatusFunc(candidates *proxy.Candidates) func() proxy.Status {
	return candidates.QuarantineStatus
}

// A read through a nested field is still a read.
func Reason(state *proxy.State) string { // want `parameter state holds State only to read Status; depend on that instead`
	return state.Status.Reason
}

// Writing through a nested field or an index is a real dependency.
func Clear(state *proxy.State) {
	state.Status.Reason = ""
}

func Rename(state *proxy.State) {
	state.Reasons[0] = ""
}

func Count(state *proxy.State) {
	(state.A)++
}

func ByValue(candidates proxy.Candidates) proxy.Status { // want `parameter candidates holds Candidates only to read QuarantineStatus; depend on that instead`
	return candidates.QuarantineStatus()
}

func Small(small *proxy.Small) int {
	return small.Get()
}

// A config that is mostly unused holds fields its constructor does not need.
func Configured(config proxy.Config) string { // want `parameter config holds Config only to read URL; depend on that instead`
	return config.URL
}

func FromGenerated(generated *proxy.Generated) int {
	return generated.GetA()
}

// A function literal's signature belongs to its caller.
func Literal(all []*proxy.Candidates) int {
	return slices.IndexFunc(all, func(candidates *proxy.Candidates) bool {
		return candidates.QuarantineStatus().Reason == ""
	})
}

type local struct{ a, b, c, d, e int }

func Local(l *local) int { return l.a } // want `parameter l holds local only to read a; depend on that instead`

// A parameter of the receiver's type is the other operand.
func (l *local) less(other *local) bool { return l.a < other.a }

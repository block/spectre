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

// A value is data, not a shared object.
func ByValue(candidates proxy.Candidates) proxy.Status {
	return candidates.QuarantineStatus()
}

func Small(small *proxy.Small) int {
	return small.Get()
}

func Configured(config *proxy.Config) string {
	return config.URL
}

func Proto(message *proxy.Message) int {
	return message.GetA()
}

// A function literal's signature belongs to its caller.
func Literal(all []*proxy.Candidates) int {
	return slices.IndexFunc(all, func(candidates *proxy.Candidates) bool {
		return candidates.QuarantineStatus().Reason == ""
	})
}

// A type from the same package is not a boundary.
type local struct{ a, b, c, d, e int }

func Local(l *local) int { return l.a }

package status

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Platform inspects a service's SPECTRE mirror on a specific deploy platform:
// how the mirror is deployed, the commit it runs, and its live status.
type Platform interface {
	// Mirror reports the SPECTRE mirror state for one <env>-<region> target. It
	// fails when the platform cannot tell whether the target runs a mirror.
	Mirror(ctx context.Context, target string) (MirrorState, error)
	// ResolveCommit returns the git commit for image, or "" when unknown.
	ResolveCommit(ctx context.Context, image Image) (GitCommit, error)
	// ResolveStatus reads the live candidate quarantine status for one target.
	ResolveStatus(ctx context.Context, target string) (QuarantineReport, error)
}

// Image is a container image reference, optionally pinned with an "@" digest.
type Image string

// String returns the image reference.
func (i Image) String() string { return string(i) }

// Digest is an image's "@" content digest, or "" when the image carries none.
type Digest string

// GitCommit is the git commit an image was built from, or "" when unknown.
type GitCommit string

// String returns the commit hash.
func (c GitCommit) String() string { return string(c) }

// MirrorState is a service's SPECTRE mirror state for one deploy target.
type MirrorState struct {
	// Enabled is true when the target deploys a SPECTRE mirror.
	Enabled bool
	// Image is the mirror's full image reference, including its digest.
	Image Image
	// Digest is the mirror image's "@" digest, or "" when it carries none.
	Digest Digest
	// Commit is the git commit the mirror runs when it differs from its image's,
	// e.g. for code synced over the image, or "" to resolve it from the image.
	Commit GitCommit
}

// QuarantineReport is the live SPECTRE status read from a mirror.
type QuarantineReport struct {
	// Quarantined is true when any proxy has quarantined the candidate.
	Quarantined bool `json:"quarantined"`
	// HealthyForSeconds is how long the candidate has been healthy, measured by
	// the mirror. It is None while quarantined or when the mirror omits it.
	HealthyForSeconds Option[int64] `json:"healthy_for_seconds"`
	// Sources is the per-proxy quarantine state.
	Sources []SourceStatus `json:"sources"`
}

// SourceStatus is one proxy's quarantine state in a QuarantineReport.
type SourceStatus struct {
	// Name identifies the proxy, e.g. "ingress" or "egress".
	Name string `json:"name"`
	// Quarantined is true once this proxy has refused candidate traffic.
	Quarantined bool `json:"quarantined"`
	// Reason is the error that triggered quarantine, empty while healthy.
	Reason string `json:"reason"`
	// Since is when quarantine began, as RFC 3339, empty while healthy.
	Since string `json:"since"`
}

// PlatformFactory builds the Platform that inspects app's SPECTRE mirror.
type PlatformFactory func(app string) (Platform, error)

// Registry maps a config's platform name to the factory that builds it. A custom
// binary populates it through spectre.WithStatusPlatform.
type Registry struct {
	factories map[string]PlatformFactory
}

// NewRegistry returns an empty platform registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]PlatformFactory{}}
}

// Register adds the factory for a platform name, replacing any earlier one.
func (r *Registry) Register(name string, factory PlatformFactory) {
	r.factories[name] = factory
}

// New builds the platform named in a config for app. An unregistered name yields
// a dummy platform whose inspections fail, naming the platform the build lacks.
func (r *Registry) New(name, app string) (Platform, error) {
	factory, ok := r.factories[name]
	if !ok {
		return newDummyPlatform(name), nil
	}
	return factory(app)
}

// dummyPlatform is the stand-in for an unregistered platform. It fails every
// inspection, naming the platform the build lacks.
type dummyPlatform struct {
	name string
}

// newDummyPlatform returns the stand-in for the unregistered platform name.
func newDummyPlatform(name string) dummyPlatform {
	return dummyPlatform{name: name}
}

// Mirror fails, because a dummy platform cannot inspect a mirror.
func (d dummyPlatform) Mirror(context.Context, string) (MirrorState, error) {
	return MirrorState{}, errors.Errorf("no status platform %q registered in this build", d.name)
}

// ResolveCommit fails, because a dummy platform resolves nothing.
func (d dummyPlatform) ResolveCommit(context.Context, Image) (GitCommit, error) {
	return "", errors.Errorf("no status platform %q registered in this build", d.name)
}

// ResolveStatus fails, because a dummy platform reaches no mirror.
func (d dummyPlatform) ResolveStatus(context.Context, string) (QuarantineReport, error) {
	return QuarantineReport{}, errors.Errorf("no status platform %q registered in this build", d.name)
}

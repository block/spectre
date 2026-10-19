package status

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestRegistryNewRegistered(t *testing.T) {
	registry := NewRegistry()
	var want Platform = stubPlatform{}
	registry.Register("ski", func(app string) (Platform, error) {
		assert.Equal(t, "feeplans-fe", app)
		return want, nil
	})

	platform, err := registry.New("ski", "feeplans-fe")
	assert.NoError(t, err)
	assert.Equal(t, want, platform)
}

func TestRegistryNewUnknownIsDummy(t *testing.T) {
	platform, err := NewRegistry().New("bogus", "feeplans-fe")
	assert.NoError(t, err)

	// A dummy fails every inspection, naming the platform the build lacks.
	_, err = platform.Mirror(context.Background(), "staging-us-west-2")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`)
	_, err = platform.ResolveCommit(context.Background(), "image")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`)
	_, err = platform.ResolveStatus(context.Background(), "staging-us-west-2")
	assert.Error(t, err)
}

// stubPlatform is a minimal Platform a test factory can return.
type stubPlatform struct{}

func (stubPlatform) Mirror(context.Context, string) (MirrorState, error) {
	return MirrorState{}, nil
}

func (stubPlatform) ResolveCommit(context.Context, Image) (GitCommit, error) { return "", nil }

func (stubPlatform) ResolveStatus(context.Context, string) (QuarantineReport, error) {
	return QuarantineReport{}, nil
}

package status

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestTargets(t *testing.T) {
	plan := Plan{Environments: []string{"staging-us-west-2", "staging-us-east-2"}}

	assert.Equal(t, []string{"staging-us-west-2", "staging-us-east-2"}, targets(Config{}, plan))
	assert.Equal(t, []string{"prod-us-west-2"}, targets(Config{Env: "prod", Region: "us-west-2"}, plan))
}

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

	// A dummy reports no mirror and fails every resolution, naming the platform.
	assert.Equal(t, MirrorState{}, platform.Mirror("staging-us-west-2"))
	_, err = platform.ResolveCommit(context.Background(), "image")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`)
	_, err = platform.ResolveStatus(context.Background(), "staging-us-west-2")
	assert.Error(t, err)
}

// stubPlatform is a minimal Platform a test factory can return.
type stubPlatform struct{}

func (stubPlatform) Mirror(string) MirrorState { return MirrorState{} }

func (stubPlatform) ResolveCommit(context.Context, string) (string, error) { return "", nil }

func (stubPlatform) ResolveStatus(context.Context, string) (QuarantineReport, error) {
	return QuarantineReport{}, nil
}

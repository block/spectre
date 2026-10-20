package status

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
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

func TestCommitText(t *testing.T) {
	mirror := MirrorState{Image: "registry/app@sha256:abc", Digest: "sha256:abc"}
	synced := mirror
	synced.Commit = "def456"
	for _, test := range []struct {
		name     string
		mirror   MirrorState
		platform Platform
		want     string
	}{
		{"Resolved", mirror, commitPlatform{commit: "abc123"}, "abc123"},
		{"Unresolved", mirror, commitPlatform{}, "unknown (digest sha256:abc)"},
		{"ResolveError", mirror, commitPlatform{err: errors.New("boom")}, "unknown (resolve error: boom, digest sha256:abc)"},
		{"MirrorCommit", synced, commitPlatform{err: errors.New("not called")}, "def456"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, commitText(context.Background(), test.platform, test.mirror))
		})
	}
}

// commitPlatform is a stubPlatform that resolves every image to commit, or fails with err.
type commitPlatform struct {
	stubPlatform
	commit GitCommit
	err    error
}

func (p commitPlatform) ResolveCommit(context.Context, Image) (GitCommit, error) {
	return p.commit, p.err
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

package scaffold

import (
	"runtime/debug"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestSpectreVersion(t *testing.T) {
	tests := []struct {
		name    string
		info    debug.BuildInfo
		version string
		ok      bool
	}{
		{name: "MainTag", info: debug.BuildInfo{Main: debug.Module{Path: "github.com/block/spectre", Version: "v1.1.0"}}, version: "v1.1.0", ok: true},
		{name: "MainPseudo", info: debug.BuildInfo{Main: debug.Module{Path: "github.com/block/spectre", Version: "v1.1.1-0.20261023000649-9897f2d3afb3+dirty"}}, version: "9897f2d3afb3", ok: true},
		{name: "MainDevel", info: debug.BuildInfo{Main: debug.Module{Path: "github.com/block/spectre", Version: "(devel)"}}},
		{name: "Dependency", info: debug.BuildInfo{Main: debug.Module{Path: "github.com/org/cli", Version: "v0.1.0"}, Deps: []*debug.Module{{Path: "github.com/block/spectre", Version: "v1.1.0"}}}, version: "v1.1.0", ok: true},
		{name: "Replaced", info: debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/block/spectre", Version: "v1.1.0", Replace: &debug.Module{Path: "../spectre"}}}}},
		{name: "Missing", info: debug.BuildInfo{Main: debug.Module{Path: "github.com/org/cli", Version: "v0.1.0"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version, ok := spectreVersion(&test.info)
			assert.Equal(t, test.version, version)
			assert.Equal(t, test.ok, ok)
		})
	}
}

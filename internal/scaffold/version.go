package scaffold

import (
	"regexp"
	"runtime/debug"
	"strings"
)

const spectreModule = "github.com/block/spectre"

// pseudoVersion captures the commit of a Go pseudo-version.
var pseudoVersion = regexp.MustCompile(`^v[0-9.]+-(?:[0-9a-z.]+\.)?[0-9]{14}-([0-9a-f]{12})$`)

// spectreRef returns the git ref of the SPECTRE module this binary is built
// from, so templates can build the same SPECTRE. It is main when unknown.
func spectreRef() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "main"
	}
	version := info.Main.Version
	if info.Main.Path != spectreModule {
		version = ""
		for _, dep := range info.Deps {
			if dep.Path == spectreModule {
				version = dep.Version
			}
		}
	}
	version, _, _ = strings.Cut(version, "+")
	if match := pseudoVersion.FindStringSubmatch(version); match != nil {
		return match[1]
	}
	if version == "" || version == "(devel)" {
		return "main"
	}
	return version
}

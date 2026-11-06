//go:build !linux

package contain_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/contain"
)

func TestApplyRequiresLinux(t *testing.T) {
	err := contain.Apply(t.Context(), contain.Config{UID: 1002})
	assert.EqualError(t, err, "containing a user's IP traffic requires Linux")
}

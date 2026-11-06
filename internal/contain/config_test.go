package contain_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/contain"
)

func TestApplyRefusesRoot(t *testing.T) {
	err := contain.Apply(t.Context(), contain.Config{UID: 0})
	assert.EqualError(t, err, "uid must not be 0, which would contain every process running as root")
}

package supervisor

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestConfigValidate(t *testing.T) {
	valid := NewConfig()
	assert.NoError(t, valid.Validate())

	badWeight := valid
	badWeight.Limits.CandidateCPUWeight = 0
	assert.Error(t, badWeight.Validate())

	badShares := valid
	badShares.Limits.CandidateMemoryShare = 0
	badShares.Limits.ReferenceMemoryShare = 0
	assert.Error(t, badShares.Validate())

	disabled := badShares
	disabled.Limits.Enabled = false
	assert.NoError(t, disabled.Validate())

	badTimeout := valid
	badTimeout.StartupTimeout = 0
	assert.Error(t, badTimeout.Validate())
}

func TestMemorySplit(t *testing.T) {
	even := LimitConfig{CandidateMemoryShare: 1, ReferenceMemoryShare: 1}
	candidate, reference := even.memorySplit(1000)
	assert.Equal(t, int64(500), candidate)
	assert.Equal(t, int64(500), reference)

	skewed := LimitConfig{CandidateMemoryShare: 3, ReferenceMemoryShare: 1}
	candidate, reference = skewed.memorySplit(1000)
	assert.Equal(t, int64(750), candidate)
	assert.Equal(t, int64(250), reference)

	candidate, reference = even.memorySplit(0)
	assert.Equal(t, int64(0), candidate)
	assert.Equal(t, int64(0), reference)
}

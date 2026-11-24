package http

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
)

func TestConfigValidate(t *testing.T) {
	for name, test := range map[string]struct {
		change func(config *Config)
		err    string
	}{
		"Default":          {change: func(*Config) {}},
		"NoStartupTimeout": {change: func(config *Config) { config.CandidateStartupTimeout = 0 }, err: "candidate startup timeout must be positive"},
	} {
		t.Run(name, func(t *testing.T) {
			config := NewConfig()
			test.change(&config)
			err := config.Validate()
			if test.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.err)
		})
	}
}

func TestEgressConfig(t *testing.T) {
	config := NewConfig()
	config.ReferenceEgressListen = "unix:/sockets/egress-reference.sock"
	config.CandidateEgressListen = "unix:/sockets/egress-candidate.sock"
	config.Destinations = map[string]string{"b.example": "http://b", "a.example": "http://a"}
	config.Descriptors.DescriptorsDir = "descriptors"
	expected := NewEgressConfig()
	expected.ReferenceListen = "unix:/sockets/egress-reference.sock"
	expected.CandidateListen = "unix:/sockets/egress-candidate.sock"
	expected.Destinations = config.Destinations
	expected.Descriptors = config.Descriptors
	assert.Equal(t, expected, config.egressConfig())
}

func TestIngressConfig(t *testing.T) {
	config := NewConfig()
	config.Listen = "127.0.0.1:50050"
	config.ReferenceURL = "h2c+unix:/sockets/reference.sock"
	config.CandidateURL = "http+unix:/sockets/candidate.sock"
	config.Reflection = false
	config.CandidateStartupTimeout = time.Minute
	config.Descriptors.DescriptorsDir = "descriptors"
	expected := NewIngressConfig()
	expected.Listen = "127.0.0.1:50050"
	expected.Reference = "h2c+unix:/sockets/reference.sock"
	expected.Candidate = "http+unix:/sockets/candidate.sock"
	expected.Reflection = false
	expected.CandidateStartupTimeout = time.Minute
	expected.Descriptors = config.Descriptors
	assert.Equal(t, expected, config.ingressConfig())
}

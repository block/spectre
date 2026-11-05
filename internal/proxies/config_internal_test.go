package proxies

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestConfigValidate(t *testing.T) {
	for name, test := range map[string]struct {
		change func(config *Config)
		err    string
	}{
		"Default":           {change: func(*Config) {}},
		"NoMatchWindow":     {change: func(config *Config) { config.MatchWindow = 0 }, err: "match window must be positive"},
		"NoStartupTimeout":  {change: func(config *Config) { config.CandidateStartupTimeout = 0 }, err: "candidate startup timeout must be positive"},
		"NoServerKeepalive": {change: func(config *Config) { config.Server.KeepaliveInterval = 0 }, err: "server keepalive interval and stream max age must be positive"},
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

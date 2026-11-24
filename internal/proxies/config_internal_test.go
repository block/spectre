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
		"NoCandidateLimit":  {change: func(config *Config) { config.Candidates.CandidateMaxInFlight = 0 }, err: "candidate maximum in-flight requests must be positive"},
		"NoServerKeepalive": {change: func(config *Config) { config.Server.KeepaliveInterval = 0 }, err: "server keepalive interval and stream max age must be positive"},
		"NoStatsdForward":   {change: func(config *Config) { config.Statsd.ReferenceListen = "unix:@statsd.sock" }, err: "StatsD reference listener needs a forward address"},
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

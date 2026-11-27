package statsd_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/protocols/statsd"
)

func TestConfigValidate(t *testing.T) {
	for name, test := range map[string]struct {
		configure func(config *statsd.Config)
		err       string
	}{
		"Default": {configure: func(*statsd.Config) {}},
		"Forwarded": {configure: func(config *statsd.Config) {
			config.ReferenceListen, config.Forward = "unix:@reference.sock", "unix:@veneur.sock"
		}},
		"CandidateOnly":         {configure: func(config *statsd.Config) { config.CandidateListen = "127.0.0.1:8125" }},
		"ReferenceWithoutAgent": {configure: func(config *statsd.Config) { config.ReferenceListen = "unix:@reference.sock" }, err: "StatsD reference listener needs a forward address"},
		"AgentWithoutReference": {configure: func(config *statsd.Config) { config.Forward = "unix:@veneur.sock" }, err: "StatsD forward address needs a reference listener"},
		"NoWindow":              {configure: func(config *statsd.Config) { config.Window = 0 }, err: "StatsD window must be positive"},
		"NegativeSettle":        {configure: func(config *statsd.Config) { config.Settle = -1 }, err: "StatsD settle delay and candidate startup timeout must not be negative"},
		"NoSeries":              {configure: func(config *statsd.Config) { config.MaxSeries = 0 }, err: "StatsD series limit must be positive"},
	} {
		t.Run(name, func(t *testing.T) {
			config := statsd.NewConfig()
			test.configure(&config)
			err := config.Validate()
			if test.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.err)
		})
	}
}

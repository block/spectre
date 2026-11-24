package statsd_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/statsd"
)

func TestConfigValidate(t *testing.T) {
	for name, test := range map[string]struct {
		config statsd.Config
		err    string
	}{
		"Default":               {config: statsd.NewConfig()},
		"Forwarded":             {config: statsd.Config{ReferenceListen: "unix:@reference.sock", Forward: "unix:@veneur.sock"}},
		"CandidateOnly":         {config: statsd.Config{CandidateListen: "127.0.0.1:8125"}},
		"ReferenceWithoutAgent": {config: statsd.Config{ReferenceListen: "unix:@reference.sock"}, err: "StatsD reference listener needs a forward address"},
		"AgentWithoutReference": {config: statsd.Config{Forward: "unix:@veneur.sock"}, err: "StatsD forward address needs a reference listener"},
	} {
		t.Run(name, func(t *testing.T) {
			err := test.config.Validate()
			if test.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.err)
		})
	}
}

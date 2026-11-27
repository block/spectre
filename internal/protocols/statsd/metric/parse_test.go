package metric_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/protocols/statsd/metric"
)

func TestParseReadsDogStatsDLines(t *testing.T) {
	for name, test := range map[string]struct {
		datagram string
		samples  []metric.Sample
		err      string
	}{
		"CounterWithRateAndTags": {
			datagram: "framework.http.response_codes:2|c|@0.5|#code:200,controller:fees,canary",
			samples: []metric.Sample{{
				Series: metric.Series{Name: "framework.http.response_codes", Type: metric.Counter, Tags: map[string]string{"code": "200", "controller": "fees", "canary": ""}},
				Values: []float64{2},
				Rate:   0.5,
			}},
		},
		"TimerWithManyValues": {
			datagram: "response_times:12:15.5|ms",
			samples:  []metric.Sample{{Series: metric.Series{Name: "response_times", Type: metric.Timer, Tags: map[string]string{}}, Values: []float64{12, 15.5}, Rate: 1}},
		},
		"SetAndGaugeOnSeparateLines": {
			datagram: "users:alice|s\nqueue.depth:3|g|T1700000000\n",
			samples: []metric.Sample{
				{Series: metric.Series{Name: "users", Type: metric.Set, Tags: map[string]string{}}, Members: []string{"alice"}, Rate: 1},
				{Series: metric.Series{Name: "queue.depth", Type: metric.Gauge, Tags: map[string]string{}}, Values: []float64{3}, Rate: 1},
			},
		},
		"EventsAndServiceChecksSkipped": {
			datagram: "_e{5,4}:title|text\n_sc|check|0\nup:1|c",
			samples:  []metric.Sample{{Series: metric.Series{Name: "up", Type: metric.Counter, Tags: map[string]string{}}, Values: []float64{1}, Rate: 1}},
		},
		"MalformedLinesSkipped": {
			datagram: "noValue|c\nbad:x|c\nup:1|c\nunknown:1|q\nrate:1|c|@2\nnan:NaN|ms\ninf:+Inf|g",
			samples:  []metric.Sample{{Series: metric.Series{Name: "up", Type: metric.Counter, Tags: map[string]string{}}, Values: []float64{1}, Rate: 1}},
			err: `metric "noValue|c" has no value
metric "bad" has invalid value "x"
metric "unknown" has unknown type "q"
metric "rate" has invalid sample rate "@2"
metric "nan" has invalid value "NaN"
metric "inf" has invalid value "+Inf"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			samples, err := metric.Parse([]byte(test.datagram))
			assert.Equal(t, test.samples, samples)
			if test.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.err)
		})
	}
}

// Timers, histograms and distributions compare alike, so a series' key ignores which one it is.
func TestSeriesKeyGroupsDistributionsAndSortsTags(t *testing.T) {
	timer := metric.Series{Name: "latency", Type: metric.Timer, Tags: map[string]string{"b": "2", "a": "1"}}
	histogram := metric.Series{Name: "latency", Type: metric.Histogram, Tags: map[string]string{"a": "1", "b": "2"}}
	counter := metric.Series{Name: "latency", Type: metric.Counter, Tags: map[string]string{"a": "1", "b": "2"}}

	assert.Equal(t, timer.Key(), histogram.Key())
	assert.NotEqual(t, timer.Key(), counter.Key())
	assert.Equal(t, "latency{a:1,b:2}", timer.String())
}

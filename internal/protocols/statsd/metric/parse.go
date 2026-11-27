package metric

import (
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

// Sample is one metric line: its series, its values and the rate it was sampled at.
type Sample struct {
	Series Series
	// Values are the numeric values. A set's members are in Members instead.
	Values  []float64
	Members []string
	// Rate is the fraction of events the client sent, in (0, 1].
	Rate float64
}

// Weight is how many events each of the sample's values stands for.
func (s Sample) Weight() float64 {
	return 1 / s.Rate
}

// Parse reads the metric lines of a datagram. Events and service checks are skipped,
// and each malformed line is skipped and counted.
func Parse(datagram []byte) (samples []Sample, malformed int) {
	for line := range strings.SplitSeq(string(datagram), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "_e{") || strings.HasPrefix(line, "_sc|") {
			continue
		}
		sample, err := parseLine(line)
		if err != nil {
			malformed++
			continue
		}
		samples = append(samples, sample)
	}
	return samples, malformed
}

// parseLine reads name:value[:value...]|type[|@rate][|#tag,tag:value][|...].
func parseLine(line string) (Sample, error) {
	fields := strings.Split(line, "|")
	if len(fields) < 2 {
		return Sample{}, errors.Errorf("metric %q has no type", line)
	}
	name, values, found := strings.Cut(fields[0], ":")
	if !found || name == "" {
		return Sample{}, errors.Errorf("metric %q has no value", line)
	}
	metricType, known := typeOf(fields[1])
	if !known {
		return Sample{}, errors.Errorf("metric %q has unknown type %q", name, fields[1])
	}
	sample := Sample{Series: Series{Name: name, Type: metricType, Tags: map[string]string{}}, Rate: 1}
	for _, field := range fields[2:] {
		switch {
		case strings.HasPrefix(field, "@"):
			rate, err := strconv.ParseFloat(field[1:], 64)
			if err != nil || rate <= 0 || rate > 1 {
				return Sample{}, errors.Errorf("metric %q has invalid sample rate %q", name, field)
			}
			sample.Rate = rate
		case strings.HasPrefix(field, "#"):
			for tag := range strings.SplitSeq(field[1:], ",") {
				if tag != "" {
					tagName, value, _ := strings.Cut(tag, ":")
					sample.Series.Tags[tagName] = value
				}
			}
		}
	}
	for value := range strings.SplitSeq(values, ":") {
		if metricType == Set {
			sample.Members = append(sample.Members, value)
			continue
		}
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return Sample{}, errors.Errorf("metric %q has invalid value %q", name, value)
		}
		sample.Values = append(sample.Values, number)
	}
	return sample, nil
}

// typeOf returns the type with a StatsD type code.
func typeOf(code string) (metricType Type, known bool) {
	switch code {
	case "c":
		return Counter, true
	case "g":
		return Gauge, true
	case "ms":
		return Timer, true
	case "h":
		return Histogram, true
	case "d":
		return Distribution, true
	case "s":
		return Set, true
	}
	return "", false
}

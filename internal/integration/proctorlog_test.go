package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Proctor starts each line with a process name and one of these markers.
const (
	outputMarker = "│"
	failedMarker = "✘"
	readyMarker  = "●"
)

// colour matches the ANSI colour codes proctor writes even when not on a terminal.
var colour = regexp.MustCompile("\x1b\\[[0-9;]*m")

// logLine is one line of proctor's output: a process's own output, or proctor's
// note about the process.
type logLine struct {
	Process string
	Marker  string
	Text    string
	// Fields holds the line's JSON object, if the process logged one.
	Fields map[string]any
}

// query selects the JSON records a process logged with every one of fields.
type query struct {
	process string
	fields  map[string]any
}

// processLog selects the records process logged with every one of fields.
func processLog(process string, fields map[string]any) query {
	return query{process: process, fields: fields}
}

// ingressLog selects the records the ingress proxy logged with every one of fields.
func ingressLog(fields map[string]any) query {
	return proxyLog("ingress", fields)
}

// egressLog selects the records the egress proxy logged with every one of fields.
func egressLog(fields map[string]any) query {
	return proxyLog("egress", fields)
}

// proxyLog selects the records of one of the proxies, which share the proxy process.
func proxyLog(subsystem string, fields map[string]any) query {
	selected := map[string]any{"spectre.subsystem": subsystem}
	for key, value := range fields {
		selected[key] = value
	}
	return processLog("proxy", selected)
}

func (q query) String() string {
	return fmt.Sprintf("%s %v", q.process, q.fields)
}

func (q query) matches(line logLine) bool {
	if line.Process != q.process || line.Marker != outputMarker {
		return false
	}
	for key, value := range q.fields {
		if line.Fields[key] != value {
			return false
		}
	}
	return true
}

// proctorLog collects proctor's output as it is written. It is safe for
// concurrent use.
type proctorLog struct {
	mu      sync.Mutex
	partial []byte
	lines   []logLine
}

// Write parses each complete line, keeping any partial line for the next write.
func (l *proctorLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, data...)
	for {
		end := bytes.IndexByte(l.partial, '\n')
		if end < 0 {
			return len(data), nil
		}
		l.lines = append(l.lines, parseLine(string(l.partial[:end])))
		l.partial = l.partial[end+1:]
	}
}

// count returns how many lines match q.
func (l *proctorLog) count(q query) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	matched := 0
	for _, line := range l.lines {
		if q.matches(line) {
			matched++
		}
	}
	return matched
}

// ready reports whether proctor has found process ready.
func (l *proctorLog) ready(process string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line.Process == process && line.Marker == readyMarker && line.Text == "ready" {
			return true
		}
	}
	return false
}

// failure returns the first line where proctor reports a process failing.
func (l *proctorLog) failure() (logLine, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line.Marker == failedMarker {
			return line, true
		}
	}
	return logLine{}, false
}

// String returns the output without colours, for a failed test's log.
func (l *proctorLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var text strings.Builder
	for _, line := range l.lines {
		text.WriteString(line.Process + " " + line.Marker + " " + line.Text + "\n")
	}
	return text.String() + string(l.partial)
}

// parseLine splits a line into its process, marker and text, and decodes text
// that is a JSON object.
func parseLine(raw string) logLine {
	process, rest, _ := strings.Cut(strings.TrimLeft(colour.ReplaceAllString(raw, ""), " "), " ")
	marker, text, _ := strings.Cut(rest, " ")
	line := logLine{Process: process, Marker: marker, Text: text}
	if marker == outputMarker && strings.HasPrefix(text, "{") {
		_ = json.Unmarshal([]byte(text), &line.Fields)
	}
	return line
}

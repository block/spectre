package supervisor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestServiceLogWriterLines(t *testing.T) {
	for _, test := range []struct {
		name     string
		line     string
		expected string
	}{
		{name: "Text", line: "listening on :8080", expected: "candidate: listening on :8080"},
		{name: "JSON", line: `{"severity":"WARNING","ts":1720000000000,"message":"disk full"}`,
			expected: `{"spectre.subsystem":"candidate","severity":"WARNING","ts":1720000000000,"message":"disk full"}`},
		{name: "EmptyObject", line: `{}`, expected: `{"spectre.subsystem":"candidate"}`},
		{name: "EmptyObjectSpaced", line: ` { } `, expected: ` {"spectre.subsystem":"candidate" } `},
		{name: "Spacing", line: `  { "a" : 1 }`, expected: `  {"spectre.subsystem":"candidate", "a" : 1 }`},
		{name: "CRLF", line: "{\"a\":1}\r", expected: "{\"spectre.subsystem\":\"candidate\",\"a\":1}\r"},
		{name: "Malformed", line: `{"a":`, expected: `candidate: {"a":`},
		{name: "Array", line: `[1,2]`, expected: `candidate: [1,2]`},
		{name: "Blank", line: "", expected: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := newServiceLogWriter(&output, roleCandidate)
			n, err := writer.Write([]byte(test.line + "\n"))
			assert.NoError(t, err)
			assert.Equal(t, len(test.line)+1, n)
			assert.Equal(t, test.expected+"\n", output.String())
		})
	}
}

func TestServiceLogWriterSplitWrites(t *testing.T) {
	var output bytes.Buffer
	writer := newServiceLogWriter(&output, roleReference)
	for _, chunk := range []string{`{"msg":`, `"one"}` + "\nsecond\n", "third"} {
		_, err := writer.Write([]byte(chunk))
		assert.NoError(t, err)
	}
	// The unterminated third line is held back until the writer is flushed.
	assert.Equal(t, `{"spectre.subsystem":"reference","msg":"one"}`+"\nreference: second\n", output.String())
	assert.NoError(t, writer.Flush())
	assert.Equal(t, `{"spectre.subsystem":"reference","msg":"one"}`+"\nreference: second\nreference: third\n", output.String())
}

func TestServiceLogWriterBoundsUnterminatedOutput(t *testing.T) {
	var output bytes.Buffer
	writer := newServiceLogWriter(&output, roleCandidate)
	// Progress output: carriage returns but never a newline.
	chunk := []byte(strings.Repeat("progress 42%\r", 100))
	for written := 0; written < 2*maxServiceLine+10; written += len(chunk) {
		_, err := writer.Write(chunk)
		assert.NoError(t, err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	assert.Equal(t, 2, len(lines))
	for _, line := range lines {
		assert.Equal(t, len("candidate: ")+maxServiceLine, len(line))
	}
	assert.True(t, len(writer.pending) < maxServiceLine, "pending output is not bounded")
}

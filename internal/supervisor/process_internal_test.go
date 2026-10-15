package supervisor

import (
	"bytes"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestTagWriterTextPrefix(t *testing.T) {
	var out bytes.Buffer
	w := newTagWriter(&out, "ingress", false)
	_, err := w.Write([]byte("hello\nworld\n"))
	assert.NoError(t, err)
	assert.Equal(t, "ingress | hello\ningress | world\n", out.String())
}

func TestTagWriterJSONInjectsProcess(t *testing.T) {
	var out bytes.Buffer
	w := newTagWriter(&out, "ingress", true)
	_, err := w.Write([]byte(`{"level":"ERROR","msg":"Candidate quarantined"}` + "\n"))
	assert.NoError(t, err)
	assert.Equal(t, `{"process":"ingress","level":"ERROR","msg":"Candidate quarantined"}`+"\n", out.String())
}

func TestTagWriterJSONEmptyObject(t *testing.T) {
	var out bytes.Buffer
	w := newTagWriter(&out, "egress", true)
	_, err := w.Write([]byte("{}\n"))
	assert.NoError(t, err)
	assert.Equal(t, `{"process":"egress"}`+"\n", out.String())
}

func TestTagWriterJSONPassesThroughNonObject(t *testing.T) {
	var out bytes.Buffer
	w := newTagWriter(&out, "candidate", true)
	_, err := w.Write([]byte("plain text line\n"))
	assert.NoError(t, err)
	assert.Equal(t, "plain text line\n", out.String())
}

func TestTagWriterBuffersPartialLines(t *testing.T) {
	var out bytes.Buffer
	w := newTagWriter(&out, "reference", true)
	_, err := w.Write([]byte(`{"msg":"part`))
	assert.NoError(t, err)
	assert.Equal(t, "", out.String())
	_, err = w.Write([]byte(`ial"}` + "\n"))
	assert.NoError(t, err)
	assert.Equal(t, `{"process":"reference","msg":"partial"}`+"\n", out.String())
}

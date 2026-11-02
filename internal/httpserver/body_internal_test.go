package httpserver

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/proxy"
)

func TestStreamBodyAbortsBeforeReaderSeesIncompleteBody(t *testing.T) {
	type readResult struct {
		err          error
		abortedFirst bool
	}
	readDone := make(chan struct{})
	var aborted atomic.Bool
	body := newStreamBody(proxy.NewBudget(bufferBlockSize), func(error) {}, func() {
		// Waiting lets a reader that is not held back finish first and fail the test.
		select {
		case <-readDone:
		case <-time.After(50 * time.Millisecond):
		}
		aborted.Store(true)
	})
	results := make(chan readResult, 1)
	go func() {
		_, err := body.Read(make([]byte, 1))
		results <- readResult{err: err, abortedFirst: aborted.Load()}
		close(readDone)
	}()

	body.closeWriter(io.ErrUnexpectedEOF)
	result := <-results
	assert.IsError(t, result.err, io.ErrUnexpectedEOF)
	assert.True(t, result.abortedFirst, "reader saw the incomplete body before the candidate was aborted")
}

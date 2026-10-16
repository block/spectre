package future_test

import (
	"context"
	"sync"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/future"
)

func TestFutureKeepsFirstValue(t *testing.T) {
	value := future.New[string]()
	_, ready := value.Get()
	assert.False(t, ready)

	assert.True(t, value.Set("first"))
	assert.False(t, value.Set("second"))

	got, ready := value.Get()
	assert.True(t, ready)
	assert.Equal(t, "first", got)
	got, err := value.Wait(t.Context())
	assert.NoError(t, err)
	assert.Equal(t, "first", got)
}

func TestFutureWaitStopsWithContext(t *testing.T) {
	value := future.New[string]()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := value.Wait(ctx)
	assert.IsError(t, err, context.Canceled)
}

func TestFutureConcurrentSetters(t *testing.T) {
	value := future.New[int]()
	const setters = 16
	waited := make(chan int, setters)
	stored := make(chan int, setters)
	var group sync.WaitGroup
	for i := range setters {
		group.Go(func() {
			got, err := value.Wait(t.Context())
			assert.NoError(t, err)
			waited <- got
		})
		group.Go(func() {
			if value.Set(i) {
				stored <- i
			}
		})
	}
	group.Wait()
	close(waited)
	close(stored)

	assert.Equal(t, 1, len(stored))
	first := <-stored
	for got := range waited {
		assert.Equal(t, first, got)
	}
}

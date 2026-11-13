package protocol

import (
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
)

// Withdrawing a source edits its queue in place while candidates choose from it.
func TestEgressChoosesSourceWhileItIsWithdrawn(t *testing.T) {
	t.Parallel()
	const candidates = 8
	hash, sourceHash := comparison.Hash{1}, comparison.Hash{2}
	for range 200 {
		q := &egressQueue{
			window:   time.Millisecond,
			unused:   map[comparison.Hash][]*egressExchange{},
			waiting:  map[comparison.Hash][]*egressWaiter{},
			offered:  map[comparison.Hash][]*egressExchange{},
			deriving: map[comparison.Hash][]*egressWaiter{},
		}
		source := newEgressExchange(1, true)
		q.mu.Lock()
		q.offered[sourceHash] = []*egressExchange{source}
		q.mu.Unlock()
		start := make(chan struct{})
		chosen := make(chan *egressExchange, candidates)
		var wg sync.WaitGroup
		for arrival := range uint64(candidates) {
			wg.Go(func() {
				<-start
				choice, err := q.choose(t.Context(), hash, Some(sourceHash), newEgressWaiter(arrival+2))
				if err == nil {
					chosen <- choice.pending
				}
			})
		}
		wg.Go(func() {
			<-start
			q.withdraw(sourceHash, source)
		})
		close(start)
		wg.Wait()
		close(chosen)

		for pending := range chosen {
			assert.Equal(t, source, pending)
		}
	}
}

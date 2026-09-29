package proxy

import "golang.org/x/sync/semaphore"

// Budget caps buffered memory shared across every in-flight request.
// Reservations never block, so a full budget fails immediately.
type Budget struct {
	limit     int
	available *semaphore.Weighted
}

// NewBudget returns a budget that allows at most limit reserved bytes.
func NewBudget(limit int) *Budget {
	return &Budget{limit: limit, available: semaphore.NewWeighted(int64(limit))}
}

// Limit reports the total number of bytes the budget allows.
func (b *Budget) Limit() int {
	return b.limit
}

// Reserve claims size bytes and reports whether they fit in the remaining budget.
func (b *Budget) Reserve(size int) (reserved bool) {
	return b.available.TryAcquire(int64(size))
}

// Release returns size previously reserved bytes to the budget.
// Releasing more than is reserved panics.
func (b *Budget) Release(size int) {
	b.available.Release(int64(size))
}

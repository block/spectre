package proxy

import "context"

// Status is what a quarantine reports.
type Status struct{ Reason string }

// Candidates has many members, most of them behaviour.
type Candidates struct {
	status Status
	runs   int
}

func (c *Candidates) Start(ctx context.Context) bool { return true }
func (c *Candidates) Finish()                         {}
func (c *Candidates) Quarantine(reason string)         {}
func (c *Candidates) Quarantined() <-chan struct{}     { return nil }
func (c *Candidates) QuarantineStatus() Status         { return c.status }
func (c *Candidates) Shutdown(ctx context.Context) error {
	return nil
}
func (c *Candidates) close()       {}
func (c *Candidates) Flush() error { return nil }

// Wrapped has many getters but one field, so it is small.
type Wrapped struct{ value int }

func (w Wrapped) Value() int  { return w.value }
func (w Wrapped) Double() int { return w.value * 2 }
func (w Wrapped) Triple() int { return w.value * 3 }
func (w Wrapped) Square() int { return w.value * w.value }

// State has enough data fields to be worth narrowing.
type State struct {
	Status  Status
	Reasons []string
	A, B, C int
}

// Small has too few members to be worth narrowing.
type Small struct{ Value int }

func (s *Small) Get() int { return s.Value }

// Config is a Kong config.
type Config struct {
	URL     string `help:"URL."`
	Service string `help:"Service."`
	A, B, C int
}

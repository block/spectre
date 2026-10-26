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
func (c *Candidates) close() {}

// Small has too few members to be worth narrowing.
type Small struct{ Value int }

func (s *Small) Get() int { return s.Value }

// Config is a Kong config, which constructors take whole.
type Config struct {
	URL     string `help:"URL."`
	Service string `help:"Service."`
	A, B, C int
}

// Message is a protobuf message, which is plain data.
type Message struct{ a, b, c, d, e int }

func (m *Message) ProtoReflect() int { return 0 }
func (m *Message) GetA() int         { return m.a }

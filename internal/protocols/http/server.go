package http

import (
	"context"
	"net"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpservice"
	"github.com/block/spectre/internal/logger"
)

// The listener names, which Provider.Listens and server.Serve agree on.
const (
	// IngressListener receives client traffic.
	IngressListener = "ingress"
	// ReferenceEgressListener receives the reference's dependency calls.
	ReferenceEgressListener = "reference-egress"
	// CandidateEgressListener receives the candidate's dependency calls.
	CandidateEgressListener = "candidate-egress"
)

const (
	// The log subsystems of the two proxies.
	ingressSubsystem = "ingress"
	egressSubsystem  = "egress"
)

// server serves ingress and egress together, and stops both when either stops.
// Ingress stays unready until both backends accept, so no mirrored traffic starts early.
type server struct {
	ingress    *Ingress
	egress     *Egress
	transports []*httpservice.Transport
}

// newServer serves ingress and egress, and closes the idle connections of transports once they stop.
func newServer(ingress *Ingress, egress *Egress, transports ...*httpservice.Transport) *server {
	return &server{ingress: ingress, egress: egress, transports: transports}
}

// Serve runs both proxies until ctx is done or one fails, then stops the other.
func (s *server) Serve(ctx context.Context, listeners map[string]net.Listener) error {
	defer func() {
		for _, transport := range s.transports {
			transport.CloseIdleConnections()
		}
	}()
	ingress, reference, candidate := listeners[IngressListener], listeners[ReferenceEgressListener], listeners[CandidateEgressListener]
	if ingress == nil || reference == nil || candidate == nil {
		return errors.New("HTTP listeners are not open")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- s.egress.Serve(logger.WithSubsystem(ctx, egressSubsystem), reference, candidate) }()
	go func() { results <- s.ingress.Serve(logger.WithSubsystem(ctx, ingressSubsystem), ingress) }()
	first := <-results
	cancel()
	return errors.Join(first, <-results)
}

package coordinator

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"
	"go.jetify.com/typeid"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/block/spectre/internal/coordinator/pb"
	"github.com/block/spectre/internal/coordinator/pb/coordinatorpbconnect"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/quarantine"
)

type instancePrefix struct{}

func (instancePrefix) Prefix() string { return "mirror" }

// instanceID identifies one mirror process.
type instanceID struct {
	typeid.TypeID[instancePrefix]
}

// Source is a proxy whose quarantine state a mirror reports.
type Source struct {
	Name       string
	Candidates *proxy.Candidates
}

// Reporter sends a mirror's whole state to the coordination service when it
// starts, on every heartbeat, and as soon as a source quarantines.
type Reporter struct {
	config   ReporterConfig
	client   coordinatorpbconnect.MirrorServiceClient
	instance instanceID
	sources  []Source
}

// NewReporter reports the state of a mirror's sources.
func NewReporter(config ReporterConfig, sources ...Source) (*Reporter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() {
		return nil, errors.New("a coordination service URL is required to report")
	}
	instance, err := typeid.New[instanceID]()
	if err != nil {
		return nil, errors.Wrap(err, "identify mirror instance")
	}
	return &Reporter{
		config:   config,
		client:   coordinatorpbconnect.NewMirrorServiceClient(&http.Client{}, config.Client.URL),
		instance: instance,
		sources:  sources,
	}, nil
}

// Run reports until ctx is cancelled. A failed report is logged and retried
// on the next heartbeat, so the coordination service never blocks the mirror.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.config.Heartbeat)
	defer ticker.Stop()
	quarantined := r.watchQuarantines(ctx)
	for {
		r.report(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-quarantined:
		}
	}
}

// watchQuarantines signals once for each source that quarantines.
func (r *Reporter) watchQuarantines(ctx context.Context) <-chan struct{} {
	quarantined := make(chan struct{}, len(r.sources))
	for _, source := range r.sources {
		go func() {
			select {
			case <-source.Candidates.Quarantined():
				quarantined <- struct{}{}
			case <-ctx.Done():
			}
		}()
	}
	return quarantined
}

func (r *Reporter) report(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, r.config.Heartbeat)
	defer cancel()
	_, err := r.client.Report(ctx, connect.NewRequest(&pb.ReportRequest{Mirror: r.mirror()}))
	if err != nil && ctx.Err() == nil {
		logger.FromContext(ctx).WarnContext(ctx, "Failed to report to the coordination service", "error", err)
	}
}

// mirror snapshots the mirror's current state.
func (r *Reporter) mirror() *pb.Mirror {
	sources := make([]*pb.Source, 0, len(r.sources))
	for _, source := range r.sources {
		reported := &pb.Source{Name: source.Name}
		if status, ok := source.Candidates.QuarantineStatus().Get(); ok {
			reported.Quarantine = quarantineMessage(status)
		}
		sources = append(sources, reported)
	}
	return &pb.Mirror{
		Instance:    r.instance.String(),
		Service:     r.config.Service,
		Environment: r.config.Environment,
		Revision:    r.config.Revision,
		Sources:     sources,
	}
}

func quarantineMessage(status proxy.QuarantineStatus) *pb.Quarantine {
	reason := status.Reason
	message := &pb.Quarantine{
		Since:       timestamppb.New(status.Since),
		Kind:        string(reason.Kind),
		Differences: reason.Differences,
		Detail:      reason.Detail,
	}
	if traffic, ok := reason.Traffic.Get(); ok {
		message.Traffic = trafficMessage(traffic)
	}
	if node, ok := reason.Node.Get(); ok {
		message.Node = &node
	}
	return message
}

func trafficMessage(traffic quarantine.Traffic) *pb.Traffic {
	return &pb.Traffic{Direction: string(traffic.Direction), Protocol: string(traffic.Protocol), Route: string(traffic.Route)}
}

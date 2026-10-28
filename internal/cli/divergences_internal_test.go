package cli

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

func TestDivergencesListCommand(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService().Handler())
	t.Cleanup(httpServer.Close)
	rpc := spectrepbconnect.NewSpectreServiceClient(httpServer.Client(), httpServer.URL)
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	divergent := &pb.Divergence{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   pb.Direction_DIRECTION_INGRESS,
		Quarantine: &pb.Quarantine{
			Since:       timestamppb.New(first.Add(time.Minute)),
			Kind:        pb.Quarantine_KIND_DIVERGENT,
			Traffic:     &pb.Traffic{Direction: pb.Direction_DIRECTION_INGRESS, Protocol: "http", Route: "GET /v1/users"},
			Node:        proto.String("users"),
			Differences: []string{"body.name"},
		},
	}
	unmatched := &pb.Divergence{
		Service:     "users",
		Environment: "staging",
		Revision:    "abc123",
		Direction:   pb.Direction_DIRECTION_EGRESS,
		Quarantine: &pb.Quarantine{
			Since:  timestamppb.New(first),
			Kind:   pb.Quarantine_KIND_UNMATCHED,
			Detail: "no matching reference call",
		},
	}
	for _, divergence := range []*pb.Divergence{divergent, unmatched} {
		_, err := rpc.Quarantine(t.Context(), connect.NewRequest(&pb.QuarantineRequest{Divergence: divergence}))
		assert.NoError(t, err)
	}

	var text strings.Builder
	command := divergencesListCommand{Client: client.Config{URL: httpServer.URL}}
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &text}))
	assert.Equal(t, `users abc123 production
  ingress http GET /v1/users: response differences: body.name (node users)
  quarantined 2026-10-01T12:01:00Z
users abc123 staging
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z
`, text.String())

	var data strings.Builder
	command.JSON = true
	command.Node = "users"
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &data}))
	decoded := &pb.ListDivergencesResponse{}
	assert.NoError(t, protojson.Unmarshal([]byte(data.String()), decoded))
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: []*pb.Divergence{divergent}}, decoded), "%v", decoded)
}

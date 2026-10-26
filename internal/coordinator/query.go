package coordinator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/block/spectre/internal/boundary"
	pb "github.com/block/spectre/internal/coordinator/pb"
	"github.com/block/spectre/internal/coordinator/pb/coordinatorpbconnect"
	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/terminal"
)

// WriteDivergences writes the divergences the coordination service has grouped.
func WriteDivergences(ctx context.Context, w io.Writer, config DivergencesConfig, palette terminal.Palette) error {
	if err := config.Query.Validate(); err != nil {
		return err
	}
	request := &pb.ListDivergencesRequest{
		Service:     config.Service,
		Environment: config.Environment,
		Revision:    config.Revision,
		Node:        config.Node,
	}
	response, err := queryClient(config.Query).ListDivergences(ctx, connect.NewRequest(request))
	if err != nil {
		return errors.Wrap(err, "list divergences")
	}
	if config.Query.JSON {
		return writeJSON(w, response.Msg)
	}
	divergences := response.Msg.GetDivergences()
	if len(divergences) == 0 {
		_, err := fmt.Fprintln(w, palette.Dim("No divergences."))
		return errors.Wrap(err, "write divergences")
	}
	var text strings.Builder
	for _, divergence := range divergences {
		fmt.Fprintf(&text, "%s %s %s\n", palette.Bold(divergence.GetService()), divergence.GetRevision(), palette.Dim(divergence.GetEnvironment()))
		fmt.Fprintf(&text, "  %s\n", palette.Red(describe(divergence.GetLatest())))
		fmt.Fprintf(&text, "  %s\n", palette.Dim(fmt.Sprintf("%d mirrors, first seen %s, last seen %s", divergence.GetMirrors(),
			divergence.GetFirstSeen().AsTime().UTC().Format(time.RFC3339), divergence.GetLatest().GetSince().AsTime().UTC().Format(time.RFC3339))))
	}
	_, err = io.WriteString(w, text.String())
	return errors.Wrap(err, "write divergences")
}

// describe renders a quarantine as the mirror logged it, with the route and node it names.
func describe(message *pb.Quarantine) string {
	reason := reasonFromMessage(message)
	text := reason.String()
	if traffic, ok := reason.Traffic.Get(); ok {
		text = fmt.Sprintf("%s %s %s: %s", traffic.Direction, traffic.Protocol, traffic.Route, text)
	}
	if node, ok := reason.Node.Get(); ok {
		text += fmt.Sprintf(" (node %s)", node)
	}
	return text
}

// reasonFromMessage restores the reason a mirror reported. Protobuf getters
// return plain strings, so the wire format is where named types are restored.
func reasonFromMessage(message *pb.Quarantine) quarantine.Reason {
	reason := quarantine.Reason{
		Kind:        quarantine.Kind(message.GetKind()), //nolint:typeboundary // protobuf getters return plain strings
		Node:        Zero(message.GetNode()),
		Differences: message.GetDifferences(),
		Detail:      message.GetDetail(),
	}
	if traffic := message.GetTraffic(); traffic != nil {
		reason.Traffic = Some(quarantine.Traffic{
			Direction: boundary.Direction(traffic.GetDirection()), //nolint:typeboundary // protobuf getters return plain strings
			Protocol:  boundary.Protocol(traffic.GetProtocol()),   //nolint:typeboundary // protobuf getters return plain strings
			Route:     boundary.Route(traffic.GetRoute()),         //nolint:typeboundary // protobuf getters return plain strings
		})
	}
	return reason
}

func queryClient(config QueryConfig) coordinatorpbconnect.QueryServiceClient {
	return coordinatorpbconnect.NewQueryServiceClient(&http.Client{}, config.Client.URL)
}

func writeJSON(w io.Writer, message proto.Message) error {
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(message)
	if err != nil {
		return errors.Wrap(err, "encode JSON")
	}
	_, err = fmt.Fprintln(w, string(data))
	return errors.Wrap(err, "write JSON")
}

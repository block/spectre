package server

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/terminal"
	pb "github.com/block/spectre/spectrepb"
)

// WriteDivergences writes the divergences that server holds.
func WriteDivergences(ctx context.Context, w io.Writer, server *client.Client, config DivergencesConfig, palette terminal.Palette) error {
	// No reported value is empty, so an empty flag leaves its filter unset.
	request := &pb.ListDivergencesRequest{
		Service:     Zero(config.Service).Ptr(),
		Environment: Zero(config.Environment).Ptr(),
		Revision:    Zero(config.Revision).Ptr(),
		Node:        Zero(config.Node).Ptr(),
	}
	response, err := server.ListDivergences(ctx, request)
	if err != nil {
		return errors.WithStack(err)
	}
	if config.JSON {
		return writeJSON(w, response)
	}
	divergences := response.GetDivergences()
	if len(divergences) == 0 {
		_, err := fmt.Fprintln(w, palette.Dim("No divergences."))
		return errors.Wrap(err, "write divergences")
	}
	for _, divergence := range divergences {
		description, err := describe(divergence)
		if err != nil {
			return err
		}
		since := "quarantined " + divergence.GetQuarantine().GetSince().AsTime().UTC().Format(time.RFC3339)
		_, err = fmt.Fprintf(w, "%s %s %s\n  %s\n  %s\n", palette.Bold(divergence.GetService()), divergence.GetRevision(),
			palette.Dim(divergence.GetEnvironment()), palette.Red(description), palette.Dim(since))
		if err != nil {
			return errors.Wrap(err, "write divergences")
		}
	}
	return nil
}

// describe renders a quarantine as the mirror logged it, with the direction,
// route, and node it names.
func describe(divergence *pb.Divergence) (string, error) {
	direction, err := directionFromMessage(divergence.GetDirection())
	if err != nil {
		return "", errors.Wrap(err, "describe divergence")
	}
	reason, err := reasonFromMessage(divergence.GetQuarantine())
	if err != nil {
		return "", errors.Wrap(err, "describe divergence")
	}
	text := fmt.Sprintf("%s: %s", direction, reason)
	if traffic, ok := reason.Traffic.Get(); ok {
		text = fmt.Sprintf("%s %s %s: %s", direction, traffic.Protocol, traffic.Route, reason)
	}
	if node, ok := reason.Node.Get(); ok {
		text += fmt.Sprintf(" (node %s)", node)
	}
	return text, nil
}

func writeJSON(w io.Writer, message proto.Message) error {
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(message)
	if err != nil {
		return errors.Wrap(err, "encode JSON")
	}
	_, err = fmt.Fprintln(w, string(data))
	return errors.Wrap(err, "write JSON")
}

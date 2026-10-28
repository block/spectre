package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	pb "github.com/block/spectre/spectrepb"
)

// divergencesCommand groups the commands for the divergences mirrors reported.
type divergencesCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
	List  divergencesListCommand  `cmd:"" default:"withargs" help:"List the divergences that mirrors reported. This is the default."`
	Clear divergencesClearCommand `cmd:"" help:"Clear the divergences of a service environment once they are dealt with."`
}

// divergencesListCommand lists the divergences mirrors reported.
type divergencesListCommand struct {
	Client      client.Config `embed:""`
	JSON        bool          `help:"Print JSON instead of text."`
	Service     string        `help:"Only list divergences of this service."`
	Environment string        `help:"Only list divergences in this environment."`
	Revision    string        `help:"Only list divergences of this candidate revision."`
	Node        string        `help:"Only list divergences on routes of this migration state node."`
}

func (c *divergencesListCommand) Run(ctx context.Context, out Output) error {
	// No reported value is empty, so an empty flag leaves its filter unset.
	request := &pb.ListDivergencesRequest{
		Service:     Zero(c.Service).Ptr(),
		Environment: Zero(c.Environment).Ptr(),
		Revision:    Zero(c.Revision).Ptr(),
		Node:        Zero(c.Node).Ptr(),
	}
	response, err := client.New(c.Client).ListDivergences(ctx, request)
	if err != nil {
		return errors.WithStack(err)
	}
	if c.JSON {
		return writeJSON(out, response)
	}
	divergences := response.GetDivergences()
	palette := out.Palette
	if len(divergences) == 0 {
		_, err := fmt.Fprintln(out.Writer, palette.Dim("No divergences."))
		return errors.Wrap(err, "write divergences")
	}
	for _, divergence := range divergences {
		description, err := describe(divergence)
		if err != nil {
			return err
		}
		since := "quarantined " + divergence.GetQuarantine().GetSince().AsTime().UTC().Format(time.RFC3339)
		_, err = fmt.Fprintf(out.Writer, "%s %s %s\n  %s\n  %s\n", palette.Bold(divergence.GetService()), divergence.GetRevision(),
			palette.Dim(divergence.GetEnvironment()), palette.Red(description), palette.Dim(since))
		if err != nil {
			return errors.Wrap(err, "write divergences")
		}
	}
	return nil
}

// divergencesClearCommand clears the divergences of a service environment.
type divergencesClearCommand struct {
	Clear server.ClearConfig `embed:""`
}

func (c *divergencesClearCommand) Run(ctx context.Context) error {
	return errors.WithStack(client.New(c.Clear.Client).ClearDivergences(ctx, c.Clear.Service, c.Clear.Environment))
}

// describe renders a quarantine as the mirror logged it, with the direction,
// route, and node it names.
func describe(divergence *pb.Divergence) (string, error) {
	direction, err := server.DirectionFromMessage(divergence.GetDirection())
	if err != nil {
		return "", errors.Wrap(err, "describe divergence")
	}
	reason, err := server.ReasonFromMessage(divergence.GetQuarantine())
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

func writeJSON(out Output, message proto.Message) error {
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(message)
	if err != nil {
		return errors.Wrap(err, "encode JSON")
	}
	_, err = fmt.Fprintln(out.Writer, string(data))
	return errors.Wrap(err, "write JSON")
}

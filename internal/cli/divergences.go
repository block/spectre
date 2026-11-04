package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/wire"
)

// divergencesCommand lists the mirrors reporting to the SPECTRE server and
// their quarantines.
type divergencesCommand struct {
	Client      client.Config `embed:""`
	JSON        bool          `help:"Print JSON instead of text."`
	Service     string        `help:"Only list mirrors of this service."`
	Environment string        `help:"Only list mirrors in this environment."`
	Revision    string        `help:"Only list mirrors running this candidate commit."`
	Node        string        `help:"Only list mirrors quarantined on routes of this migration state node."`
}

func (c *divergencesCommand) Run(ctx context.Context, out Output) error {
	// No reported value is empty, so an empty flag leaves its filter unset.
	filter := model.StateFilter{
		Service:         Zero(c.Service),
		Environment:     Zero(c.Environment),
		CandidateCommit: Zero(c.Revision),
		Node:            Zero(c.Node),
	}
	states, err := client.New(c.Client).ListStates(ctx, filter)
	if err != nil {
		return errors.WithStack(err)
	}
	if c.JSON {
		return writeJSON(out, states)
	}
	palette := out.Palette
	if len(states) == 0 {
		_, err := fmt.Fprintln(out.Writer, palette.Dim("No mirrors are reporting."))
		return errors.Wrap(err, "write divergences")
	}
	for _, state := range states {
		about := fmt.Sprintf("spectre %s, up %s", state.SpectreVersion, state.Uptime.Round(time.Second))
		var lines strings.Builder
		fmt.Fprintf(&lines, "%s %s %s %s\n", palette.Bold(state.Service), state.CandidateCommit, palette.Dim(state.Environment), palette.Dim(about))
		if len(state.Quarantines) == 0 {
			fmt.Fprintf(&lines, "  %s\n", palette.Green("healthy"))
		}
		for _, direction := range []model.Direction{model.Ingress, model.Egress} {
			quarantine, ok := state.Quarantines[direction]
			if !ok {
				continue
			}
			since := "quarantined " + quarantine.Since.UTC().Format(time.RFC3339)
			fmt.Fprintf(&lines, "  %s\n  %s\n", palette.Red(describe(direction, quarantine.Reason)), palette.Dim(since))
		}
		if _, err := fmt.Fprint(out.Writer, lines.String()); err != nil {
			return errors.Wrap(err, "write divergences")
		}
	}
	return nil
}

// describe renders a quarantine as the mirror logged it, with the direction,
// route, and node it names.
func describe(direction model.Direction, reason model.QuarantineReason) string {
	text := fmt.Sprintf("%s: %s", direction, reason)
	if traffic, ok := reason.Traffic.Get(); ok {
		text = fmt.Sprintf("%s %s %s: %s", direction, traffic.Protocol, traffic.Route, reason)
	}
	if node, ok := reason.Node.Get(); ok {
		text += fmt.Sprintf(" (node %s)", node)
	}
	return text
}

// writeJSON prints states as the SPECTRE service protocol's list response.
func writeJSON(out Output, states []model.State) error {
	data, err := wire.StatesJSON(states)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = fmt.Fprintln(out.Writer, string(data))
	return errors.Wrap(err, "write JSON")
}

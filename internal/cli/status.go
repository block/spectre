package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/cli/terminal"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/wire"
)

// statusCommand groups the commands for mirrors and their divergences. It
// owns the server URLs so a flag before "clear" is not taken by the default list command.
type statusCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
	Servers client.ServersConfig `embed:""`
	List    statusListCommand    `cmd:"" default:"withargs" help:"List the reporting mirrors and the divergences mirrors reported, from every server. This is the default."`
	Clear   statusClearCommand   `cmd:"" help:"Clear the divergences of a service environment on every server once they are dealt with."`
}

// ProvideServers gives the subcommands clients of the servers the group's flags
// locate. Kong calls it at run time, after every parse hook has set the URLs.
func (c *statusCommand) ProvideServers() *client.Servers {
	return client.NewServers(c.Servers)
}

// statusListCommand lists the reporting mirrors and recorded divergences.
type statusListCommand struct {
	JSON        bool   `help:"Print JSON instead of text."`
	Service     string `help:"Only list mirrors and divergences of this service."`
	Environment string `help:"Only list mirrors and divergences in this environment."`
	Revision    string `help:"Only list mirrors and divergences of this candidate revision."`
	Node        string `help:"Only list quarantines on routes of this migration state node."`
}

func (c *statusListCommand) Run(ctx context.Context, servers *client.Servers, out Output) error {
	// No reported value is empty, so an empty flag leaves its filter unset.
	filter := model.DivergenceFilter{
		Service:     Zero(c.Service),
		Environment: Zero(c.Environment),
		Revision:    Zero(c.Revision),
		Node:        Zero(c.Node),
	}
	status, err := servers.ListDivergences(ctx, filter)
	if err != nil {
		return errors.WithStack(err)
	}
	if c.JSON {
		return writeJSON(out, status)
	}
	mirrors := status.Environments()
	if len(mirrors) == 0 {
		_, err := fmt.Fprintln(out.Writer, out.Palette.Dim("No mirrors or divergences."))
		return errors.Wrap(err, "write divergences")
	}
	for _, mirror := range mirrors {
		if _, err := fmt.Fprint(out.Writer, renderMirror(mirror, out.Palette)); err != nil {
			return errors.Wrap(err, "write divergences")
		}
	}
	return nil
}

// statusClearCommand clears the divergences of a service environment.
type statusClearCommand struct {
	Clear server.ClearConfig `embed:""`
}

func (c *statusClearCommand) Run(ctx context.Context, servers *client.Servers) error {
	return errors.WithStack(servers.ClearDivergences(ctx, c.Clear.Service, c.Clear.Environment))
}

// renderMirror describes the mirror's state, then each recorded divergence that is
// not one of its current quarantines.
func renderMirror(m model.EnvironmentStatus, palette terminal.Palette) string {
	var text strings.Builder
	state, reporting := m.State.Get()
	if reporting {
		about := fmt.Sprintf("spectre %s, up %s", state.SpectreVersion, state.Uptime.Round(time.Second))
		fmt.Fprintf(&text, "%s %s %s %s\n", palette.Bold(m.Service), state.Revision, palette.Dim(m.Environment), palette.Dim(about))
		if len(state.Quarantines) == 0 {
			fmt.Fprintf(&text, "  %s\n", palette.Green("healthy"))
		}
		for _, direction := range []model.Direction{model.Ingress, model.Egress} {
			if quarantine, ok := state.Quarantines[direction]; ok {
				renderReason(&text, palette.Red, direction, quarantine.Reason)
				fmt.Fprintf(&text, "  %s\n", palette.Dim("quarantined "+timestamp(quarantine.Since)))
			}
		}
	} else {
		fmt.Fprintf(&text, "%s %s %s\n", palette.Bold(m.Service), palette.Dim(m.Environment), palette.Dim("not reporting"))
	}
	if total, ok := m.Total.Get(); ok {
		renderTotal(&text, total, From(state.Calls, reporting))
	} else if reporting {
		renderCalls(&text, state.Calls)
	}
	for _, divergence := range m.Divergences {
		current, ok := state.Quarantines[divergence.Direction]
		if ok && current.Since.Equal(divergence.Quarantine.Since) {
			continue
		}
		since := fmt.Sprintf("quarantined %s on %s", timestamp(divergence.Quarantine.Since), divergence.Revision)
		if reporting {
			since += ", in an earlier run"
		}
		renderReason(&text, palette.Yellow, divergence.Direction, divergence.Quarantine.Reason)
		fmt.Fprintf(&text, "  %s\n", palette.Dim(since))
	}
	return text.String()
}

// renderTotal lists accumulated calls and the reporting run's contribution.
func renderTotal(text *strings.Builder, total model.CallTotal, current Option[model.Calls]) {
	since := fmt.Sprintf("since %s (%s)", timestamp(total.Since), total.Start)
	if total.Calls.IsZero() {
		fmt.Fprintf(text, "  no calls %s\n", since)
		return
	}
	runs := "runs"
	if total.Runs == 1 {
		runs = "run"
	}
	fmt.Fprintf(text, "  calls %s, %d %s:\n", since, total.Runs, runs)
	calls, reporting := current.Get()
	suffix := func(count uint64) string {
		if !reporting {
			return ""
		}
		return fmt.Sprintf(" (%d this run)", count)
	}
	for _, route := range slices.Sorted(maps.Keys(total.Calls.Routes)) {
		fmt.Fprintf(text, "    %s: %s%s\n", route, callCount(total.Calls.Routes[route]), suffix(calls.Routes[route]))
	}
	if total.Calls.Other > 0 {
		fmt.Fprintf(text, "    other routes: %s%s\n", callCount(total.Calls.Other), suffix(calls.Other))
	}
}

// renderCalls lists the requests on each route, ordered by route.
func renderCalls(text *strings.Builder, calls model.Calls) { //nolint:narrowdeps // Formats every field of the counts.
	for _, route := range slices.Sorted(maps.Keys(calls.Routes)) {
		fmt.Fprintf(text, "    %s: %s\n", route, callCount(calls.Routes[route]))
	}
	if calls.Other > 0 {
		fmt.Fprintf(text, "    other routes: %s\n", callCount(calls.Other))
	}
}

func callCount(count uint64) string {
	if count == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", count)
}

func timestamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

// renderReason writes a quarantine reason in colour, with a divergence's
// differences on lines of their own.
func renderReason(text *strings.Builder, colour func(text string) string, direction model.Direction, reason model.QuarantineReason) {
	fmt.Fprintf(text, "  %s\n", colour(describe(direction, reason)))
	if reason.Kind != model.QuarantineDivergent {
		return
	}
	for _, line := range model.DifferenceLines(reason.Differences, reason.Truncated) {
		fmt.Fprintf(text, "    %s\n", colour(line))
	}
}

// describe summarises a quarantine with the direction, route, and node it names.
func describe(direction model.Direction, reason model.QuarantineReason) string {
	summary := reason.String()
	if reason.Kind == model.QuarantineDivergent {
		summary = reason.Compared()
	}
	text := fmt.Sprintf("%s: %s", direction, summary)
	if traffic, ok := reason.Traffic.Get(); ok {
		text = fmt.Sprintf("%s %s %s: %s", direction, traffic.Protocol, traffic.Route, summary)
	}
	if node, ok := reason.Node.Get(); ok {
		text += fmt.Sprintf(" (node %s)", node)
	}
	return text
}

// writeJSON prints states and divergences as the SPECTRE service protocol's
// list response.
func writeJSON(out Output, status model.Status) error {
	data, err := wire.ListJSON(status)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = fmt.Fprintln(out.Writer, string(data))
	return errors.Wrap(err, "write JSON")
}

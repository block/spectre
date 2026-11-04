package cli

import (
	"cmp"
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

// divergencesCommand groups the commands for mirrors and their divergences. It
// owns the server URL so a flag before "clear" is not taken by the default list command.
type divergencesCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
	Client client.Config           `embed:""`
	List   divergencesListCommand  `cmd:"" default:"withargs" help:"List the reporting mirrors and the divergences mirrors reported. This is the default."`
	Clear  divergencesClearCommand `cmd:"" help:"Clear the divergences of a service environment once they are dealt with."`
}

// ProvideClient gives the subcommands a client of the server the group's flags
// locate. Kong calls it at run time, after every parse hook has set the URL.
func (c *divergencesCommand) ProvideClient() *client.Client {
	return client.New(c.Client)
}

// divergencesListCommand lists the reporting mirrors and recorded divergences.
type divergencesListCommand struct {
	JSON        bool   `help:"Print JSON instead of text."`
	Service     string `help:"Only list mirrors and divergences of this service."`
	Environment string `help:"Only list mirrors and divergences in this environment."`
	Revision    string `help:"Only list mirrors and divergences of this candidate revision."`
	Node        string `help:"Only list quarantines on routes of this migration state node."`
}

func (c *divergencesListCommand) Run(ctx context.Context, server *client.Client, out Output) error {
	// No reported value is empty, so an empty flag leaves its filter unset.
	filter := model.DivergenceFilter{
		Service:     Zero(c.Service),
		Environment: Zero(c.Environment),
		Revision:    Zero(c.Revision),
		Node:        Zero(c.Node),
	}
	states, divergences, err := server.ListDivergences(ctx, filter)
	if err != nil {
		return errors.WithStack(err)
	}
	if c.JSON {
		return writeJSON(out, states, divergences)
	}
	mirrors := groupMirrors(states, divergences)
	if len(mirrors) == 0 {
		_, err := fmt.Fprintln(out.Writer, out.Palette.Dim("No mirrors or divergences."))
		return errors.Wrap(err, "write divergences")
	}
	for _, mirror := range mirrors {
		if _, err := fmt.Fprint(out.Writer, mirror.render(out.Palette)); err != nil {
			return errors.Wrap(err, "write divergences")
		}
	}
	return nil
}

// divergencesClearCommand clears the divergences of a service environment.
type divergencesClearCommand struct {
	Clear server.ClearConfig `embed:""`
}

func (c *divergencesClearCommand) Run(ctx context.Context, server *client.Client) error {
	return errors.WithStack(server.ClearDivergences(ctx, c.Clear.Service, c.Clear.Environment))
}

// mirror is what the server lists for one service environment: its state, if
// it is reporting, and its recorded divergences.
type mirror struct {
	Service, Environment string
	State                Option[model.State]
	Divergences          []model.Divergence
}

// groupMirrors groups states and divergences by service environment, ordered
// by service and environment.
func groupMirrors(states []model.State, divergences []model.Divergence) []*mirror {
	byKey := map[[2]string]*mirror{}
	get := func(service, environment string) *mirror {
		key := [2]string{service, environment}
		if _, ok := byKey[key]; !ok {
			byKey[key] = &mirror{Service: service, Environment: environment}
		}
		return byKey[key]
	}
	for _, state := range states {
		get(state.Service, state.Environment).State = Some(state)
	}
	for _, divergence := range divergences {
		found := get(divergence.Service, divergence.Environment)
		found.Divergences = append(found.Divergences, divergence)
	}
	mirrors := slices.Collect(maps.Values(byKey))
	slices.SortFunc(mirrors, func(a, b *mirror) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Environment, b.Environment))
	})
	return mirrors
}

// render describes the mirror's state, then each recorded divergence that is
// not one of its current quarantines.
func (m *mirror) render(palette terminal.Palette) string {
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
				fmt.Fprintf(&text, "  %s\n  %s\n", palette.Red(describe(direction, quarantine.Reason)), palette.Dim("quarantined "+timestamp(quarantine.Since)))
			}
		}
	} else {
		fmt.Fprintf(&text, "%s %s %s\n", palette.Bold(m.Service), palette.Dim(m.Environment), palette.Dim("not reporting"))
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
		fmt.Fprintf(&text, "  %s\n  %s\n", palette.Yellow(describe(divergence.Direction, divergence.Quarantine.Reason)), palette.Dim(since))
	}
	return text.String()
}

func timestamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
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

// writeJSON prints states and divergences as the SPECTRE service protocol's
// list response.
func writeJSON(out Output, states []model.State, divergences []model.Divergence) error {
	data, err := wire.ListJSON(states, divergences)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = fmt.Fprintln(out.Writer, string(data))
	return errors.Wrap(err, "write JSON")
}
